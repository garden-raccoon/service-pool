package discovery

import (
	"fmt"
	consul "github.com/hashicorp/consul/api"

	"github.com/misnaged/scriptorium/logger"

	"github.com/garden-raccoon/service-pool/service"
)

// ConsulDiscovery is a Consul implementation of
// IServiceDiscovery interface
type ConsulDiscovery struct {
	client    *consul.Client
	transport TransportProtocol
	opts      *DiscoveryOpts
}

// NewConsulDiscovery create new Consul-driven
// service Discovery
func NewConsulDiscovery(transport TransportProtocol, opts *DiscoveryOpts, addr ...string) (IServiceDiscovery, error) {
	if len(addr) != 1 {
		return nil, ErrInvalidArgumentsLength{length: len(addr), driver: DriverConsul}
	}

	config := consul.DefaultConfig()

	if addr[0] != "" {
		config.Address = addr[0]
	}

	c, err := consul.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("connect to consul discovery: %w", err)
	}

	if opts == nil {
		opts = NilDiscoveryOptions()
	}
	if opts.isOptional {
		if opts.optionalPath == "" {
			return nil, ErrEmptyOptionalPath
		}
	}

	consulDiscovery := &ConsulDiscovery{
		client:    c,
		transport: transport,
		opts:      opts,
	}

	return consulDiscovery, nil
}

// Discover and return list of the active
// blockchain addresses for requested networks
func (d *ConsulDiscovery) Discover(service string) ([]service.IService, error) {
	addrs, _, err := d.client.Health().Service(service, "", true, nil)
	if err != nil {
		return nil, fmt.Errorf("discover %s service: %w", service, err)
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("discover service via consul: %w", ErrServiceNotFound{service})
	}

	return d.createNodesFromServices(addrs), nil
}

// createNodesFromServices create addresses slice
// from consul addresses
func (d *ConsulDiscovery) createNodesFromServices(consulServices []*consul.ServiceEntry) (services []service.IService) {
	for _, s := range consulServices {
		services = append(services, d.createServiceFromConsul(s))
	}
	return
}

// createServiceFromConsul create BaseService model
// instance from consul service
func (d *ConsulDiscovery) createServiceFromConsul(srv *consul.ServiceEntry) service.IService {
	addr := d.transport.FormatAddress(srv.Service.Address)
	addr = fmt.Sprintf("%s:%d", addr, srv.Service.Port)

	if d.opts.isOptional && d.opts.optionalPath != "" {
		addr = AddEndOrRemoveFirstSlashIfNeeded(addr) + AddEndOrRemoveFirstSlashIfNeeded(d.opts.optionalPath)
	}

	logger.Log().Debug(fmt.Sprintf("discovered new service: %s", addr))

	tagsMap := make(map[string]struct{})
	for _, t := range srv.Service.Tags {
		tagsMap[t] = struct{}{}
	}

	return service.NewService(addr, srv.Service.ID, tagsMap)
}

package pool

import (
	"fmt"
	"time"

	"github.com/misnaged/scriptorium/logger"

	"github.com/garden-raccoon/service-pool/discovery"
	"github.com/garden-raccoon/service-pool/service"
)

// IServicesPool holds information about reachable
// active services, manage connections and discovery
type IServicesPool interface {
	// Start run service pool discovering
	// and healthchecks loops
	Start(healthchecks bool)

	// DiscoverServices discover all visible active
	// services via service-discovery
	DiscoverServices() error

	// NextService returns next active service
	// to take a connection
	NextService() service.IService

	// Count return numbers of
	// all healthy services in pool
	Count() int

	// List return ServicesPool ServicesList instance
	List() IServicesList

	// Close Stop all service pool
	Close()

	SetOnNewDiscCallback(f ServiceCallbackE)

	SetOnDiscRemoveCallback(f ServiceCallback)

	SetOnDiscCompletedCallback(f func())

	SetMutationNeededCallback(f ServiceCallbackB)
}

// ServicesPool holds information about reachable
// active services, manage connections and discovery
type ServicesPool struct {
	// TODO maybe is better to change this field to func
	discovery         discovery.IServiceDiscovery
	discoveryInterval time.Duration

	name string

	list IServicesList

	stop chan struct{}

	MutationFnc func(srv service.IService) (service.IService, error)

	onNewDiscCallback ServiceCallbackE

	onDiscRemoveCallback ServiceCallback

	onDiscCompletedCallback func()

	mutationNeededCallback ServiceCallbackB
}

// ServicesPoolsOpts is options that needs
// to configure ServicePool instance
type ServicesPoolsOpts struct {
	Name              string                      // service name to use in service pool
	Discovery         discovery.IServiceDiscovery // discovery interface
	DiscoveryInterval time.Duration               // reconnection interval for unreachable active rediscovery
	ListOpts          *ServicesListOpts           // service list configuration

	MutationFnc func(srv service.IService) (service.IService, error)

	CustomList IServicesList
}

type ServiceCallbackE func(srv service.IService) error
type ServiceCallback func(srv service.IService)
type ServiceCallbackB func(srv service.IService) bool

// NewServicesPool create new Services Pool
// based on given params
func NewServicesPool(opts *ServicesPoolsOpts) IServicesPool {
	pool := &ServicesPool{
		discovery:         opts.Discovery,
		discoveryInterval: opts.DiscoveryInterval,
		name:              opts.Name,
		stop:              make(chan struct{}),
		MutationFnc:       opts.MutationFnc,
	}

	if opts.CustomList != nil {
		pool.list = opts.CustomList
	} else {
		pool.list = NewServicesList(opts.Name, opts.ListOpts)

	}

	return pool
}

// Start run service pool discovering
// and healthchecks loops
func (p *ServicesPool) Start(healthchecks bool) {
	go p.discoverServicesLoop()

	if healthchecks {
		go p.list.HealthChecksLoop()
	}
}

// DiscoverServices discover all visible active
// services via service-discovery
func (p *ServicesPool) DiscoverServices() error {
	newServices, err := p.discovery.Discover(p.name)
	if err != nil {
		return fmt.Errorf("error discovering %s active: %w", p.name, err)
	}

	// construct map of newly discovered IDs
	// time complexity is O(len(newServices))
	newlyDiscoveredIDs := make(map[string]struct{})
	for _, newService := range newServices {
		newlyDiscoveredIDs[newService.ID()] = struct{}{}
	}

	// for every health service check whether it was discovered lastly
	// if not -- remove it from healthy
	// time complexity is O(len(healthy)) + O(1)
	for index, srv := range p.list.Healthy() {
		if _, wasDiscovered := newlyDiscoveredIDs[srv.ID()]; !wasDiscovered {
			p.list.RemoveFromHealthyByIndex(index)

			if p.onDiscRemoveCallback != nil {
				p.onDiscRemoveCallback(srv)
			}
			break
		}
	}

	// for every jailed service check whether it was discovered lastly
	// if not -- remove it from jailed
	// time complexity is O(len(jailed)) + O(1)
	for srvID, srv := range p.list.Jailed() {
		if _, wasDiscovered := newlyDiscoveredIDs[srvID]; !wasDiscovered {
			p.list.RemoveFromJail(srv)

			if p.onDiscRemoveCallback != nil {
				p.onDiscRemoveCallback(srv)
			}
			break
		}
	}
	// the total complexity looks like O(n), but not O(n^2) :D

	// TODO for the best scaling we need to change this part to map-based compare mechanic
	for _, newService := range newServices {
		if newService == nil {
			logger.Log().Warn("newService is nil during discovery")
			continue
		}

		// if service doesn't exist in pool or if the callback returns true --
		// then we do a mutation.
		// otherwise we prefer not to mutate srv to prevent spawning unnecessary goroutines
		isServiceExists := p.list.IsServiceExists(newService)
		weNeedToMutate := !isServiceExists || (p.mutationNeededCallback != nil && p.mutationNeededCallback(newService))
		var mutatedService service.IService

		if weNeedToMutate {
			mutatedService, err = p.MutationFnc(newService)
			if err != nil {
				logger.Log().Warn(fmt.Sprintf("mutate new discovered service: %s", err))
				continue
			}

			if p.onNewDiscCallback != nil {
				if err := p.onNewDiscCallback(mutatedService); err != nil {
					logger.Log().Warn(fmt.Sprintf("callback on new discovered service: %s", err))
				}
			}
		}

		if isServiceExists {
			continue
		}
		p.list.Add(mutatedService)
	}
	return nil
}

// NextService returns next active service
// to take a connection
func (p *ServicesPool) NextService() service.IService {
	// TODO maybe is better to return error if next service is nill
	return p.list.Next()
}

// Count return numbers of
// all healthy services in pool
func (p *ServicesPool) Count() int {
	return len(p.list.Healthy())
}

// List return ServicesPool ServicesList instance
func (p *ServicesPool) List() IServicesList {
	return p.list
}

// Close Stop all service pool
func (p *ServicesPool) Close() {
	p.list.Close()
	close(p.stop)
}

func (p *ServicesPool) SetOnNewDiscCallback(f ServiceCallbackE) {
	if p == nil {
		return
	}

	p.onNewDiscCallback = f
}

func (p *ServicesPool) SetOnDiscCompletedCallback(f func()) {
	if p == nil {
		return
	}

	p.onDiscCompletedCallback = f
}

func (p *ServicesPool) SetOnDiscRemoveCallback(f ServiceCallback) {
	if p == nil {
		return
	}

	p.onDiscRemoveCallback = f
}

func (p *ServicesPool) SetMutationNeededCallback(f ServiceCallbackB) {
	if p == nil {
		return
	}

	p.mutationNeededCallback = f
}

// discoverServicesLoop spawn discovery for
// services periodically
func (p *ServicesPool) discoverServicesLoop() {
	logger.Log().Info("start discovery loop")

	onceShuffled := false
	for {
		select {
		case <-p.stop:
			logger.Log().Warn("Stop discovery loop")
			return
		default:
			if err := p.DiscoverServices(); err != nil {
				logger.Log().Warn(fmt.Errorf("error discovery services: %w", err).Error())
			}

			// sync.Once won't work in cases when we call Start() then Close()
			// and then Start() again
			if !onceShuffled {
				p.list.Shuffle()
				onceShuffled = true

				if p.onDiscCompletedCallback != nil {
					p.onDiscCompletedCallback()
				}
			}

			Sleep(p.discoveryInterval, p.stop)
		}
	}
}

package engine

import (
	"context"
	"fmt"

	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openyurtio/openyurt/pkg/apis/raven/v1beta1"
	"github.com/openyurtio/raven/cmd/agent/app/config"
	"github.com/openyurtio/raven/pkg/networkengine/routedriver"
	"github.com/openyurtio/raven/pkg/networkengine/vpndriver"
	"github.com/openyurtio/raven/pkg/tunnelengine"
	"github.com/openyurtio/raven/pkg/utils"
)

type TunnelEngine struct {
	nodeName      string
	config        *config.Config
	client        client.Client
	option        StatusOption
	queue         workqueue.RateLimitingInterface
	routeDriver   routedriver.Driver
	vpnDriver     vpndriver.Driver
	tunnelHandler *tunnelengine.TunnelHandler
}

func newTunnelEngine(cfg *config.Config, client client.Client, opt StatusOption, queue workqueue.RateLimitingInterface) *TunnelEngine {
	return &TunnelEngine{nodeName: cfg.NodeName, config: cfg, client: client, option: opt, queue: queue}
}

func (t *TunnelEngine) worker() {
	for t.processNextWorkItem() {
	}
}

func (t *TunnelEngine) processNextWorkItem() bool {
	obj, quit := t.queue.Get()
	if quit {
		return false
	}
	gw, ok := obj.(*v1beta1.Gateway)
	if !ok {
		return false
	}
	defer t.queue.Done(gw)
	err := t.handler(gw)
	t.handleEventErr(err, gw)
	return true
}

func (t *TunnelEngine) handler(gw *v1beta1.Gateway) error {
	klog.Info(utils.FormatRavenEngine("update raven l3 tunnel config for gateway %s", gw.GetName()))
	err := t.reconcile()
	if err != nil {
		klog.Errorf("failed update tunnel driver, error %s", err.Error())
		return err
	}
	return nil
}

func (t *TunnelEngine) initDriver() error {
	routeDriver, err := routedriver.New(t.config.Tunnel.RouteDriver, t.config)
	if err != nil {
		return fmt.Errorf("fail to create route driver: %s, %s", t.config.Tunnel.RouteDriver, err)
	}
	err = routeDriver.Init()
	if err != nil {
		return fmt.Errorf("fail to initialize route driver: %s, %s", t.config.Tunnel.RouteDriver, err)
	}
	t.routeDriver = routeDriver
	klog.Info(utils.FormatRavenEngine("route driver %s initialized", t.config.Tunnel.RouteDriver))
	vpnDriver, err := vpndriver.New(t.config.Tunnel.VPNDriver, t.config)
	if err != nil {
		return fmt.Errorf("fail to create vpn driver: %s, %s", t.config.Tunnel.VPNDriver, err)
	}
	err = vpnDriver.Init()
	if err != nil {
		return fmt.Errorf("fail to initialize vpn driver: %s, %s", t.config.Tunnel.VPNDriver, err)
	}
	klog.Info(utils.FormatRavenEngine("VPN driver %s initialized", t.config.Tunnel.VPNDriver))
	t.vpnDriver = vpnDriver
	t.tunnelHandler = tunnelengine.NewTunnelHandler(t.nodeName, t.config.Tunnel.ForwardNodeIP, t.client, t.routeDriver, t.vpnDriver)
	return nil
}

func (t *TunnelEngine) clearDriver() error {
	err := t.routeDriver.Cleanup()
	if err != nil {
		klog.Errorf(utils.FormatRavenEngine("fail to cleanup route driver: %s", err.Error()))
	}
	err = t.vpnDriver.Cleanup()
	if err != nil {
		klog.Errorf(utils.FormatRavenEngine("fail to cleanup vpn driver: %s", err.Error()))
	}
	return nil
}

func (t *TunnelEngine) configGatewayListStunInfo() error {
	var gws v1beta1.GatewayList
	if err := t.client.List(context.Background(), &gws); err != nil {
		return err
	}
	for i := range gws.Items {
		// try to update info required by nat traversal
		gw := &gws.Items[i]
		if ep := getTunnelActiveEndpoints(gw); ep != nil {
			if ep.NATType == "" || ep.PublicPort == 0 {
				if err := t.configGatewayStunInfo(gw); err != nil {
					return fmt.Errorf("error config gateway nat type: %s", err)
				}
			}
		}
	}
	return nil
}

func (t *TunnelEngine) configGatewayStunInfo(gateway *v1beta1.Gateway) error {
	if getTunnelActiveEndpoints(gateway).NodeName != t.nodeName {
		return nil
	}

	natType, err := utils.GetNATType()
	if err != nil {
		return err
	}

	publicPort, err := utils.GetPublicPort()
	if err != nil {
		return err
	}

	// retry to update nat type of localGateway
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// get localGateway from api server
		var apiGw v1beta1.Gateway
		err := t.client.Get(context.Background(), client.ObjectKey{
			Name: gateway.Name,
		}, &apiGw)
		if err != nil {
			return err
		}
		for k, v := range apiGw.Spec.Endpoints {
			if v.NodeName == t.nodeName {
				apiGw.Spec.Endpoints[k].NATType = natType
				apiGw.Spec.Endpoints[k].PublicPort = publicPort
				err = t.client.Update(context.Background(), &apiGw)
				return err
			}
		}
		return nil
	})
	return err
}

func (t *TunnelEngine) reconcile() error {
	if err := t.configGatewayListStunInfo(); err != nil {
		return err
	}
	if t.routeDriver == nil || t.vpnDriver == nil {
		err := t.initDriver()
		if err != nil {
			klog.Errorf(utils.FormatRavenEngine("failed to init raven l3 tunnel engine"))
		}
	}
	err := t.tunnelHandler.Handler()
	if err != nil {
		return err
	}
	return nil
}

func (t *TunnelEngine) handleEventErr(err error, event interface{}) {
	if err == nil {
		t.queue.Forget(event)
		return
	}
	if t.queue.NumRequeues(event) < utils.MaxRetries {
		klog.Info(utils.FormatRavenEngine("error syncing event %v: %v", event, err))
		t.queue.AddRateLimited(event)
		return
	}
	klog.Info(utils.FormatRavenEngine("dropping event %q out of the queue: %v", event, err))
	t.queue.Forget(event)
}

func getTunnelActiveEndpoints(gw *v1beta1.Gateway) *v1beta1.Endpoint {
	for _, aep := range gw.Status.ActiveEndpoints {
		if aep.Type == v1beta1.Tunnel {
			return aep.DeepCopy()
		}
	}
	return nil
}

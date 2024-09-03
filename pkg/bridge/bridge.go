package bridge

import (
	"fmt"
	"net"
	"os"
	"syscall"

	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

func CreateBridge(bridge string, mtu int, gateway *net.IPNet) (netlink.Link, error) {
	if l, _ := netlink.LinkByName(bridge); l != nil {
		return l, nil
	}

	br := &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name:   bridge,
			MTU:    mtu,
			TxQLen: -1,
		},
	}

	if err := netlink.LinkAdd(br); err != nil && err != syscall.EEXIST {
		return nil, err
	}

	dev, err := netlink.LinkByName(bridge)
	if err != nil {
		return nil, err
	}

	if err := netlink.AddrAdd(dev, &netlink.Addr{IPNet: gateway}); err != nil {
		return nil, err
	}

	if err := netlink.LinkSetUp(dev); err != nil {
		return nil, err
	}

	return dev, nil
}

// 首先创建 bridge br0
// ip l a br0 type bridge
// ip l s br0 up

// 然后创建两对 veth-pair
// ip l a veth0 type veth peer name br-veth0
// ip l a veth1 type veth peer name br-veth1
//
// 分别将两对 veth-pair 加入两个 ns 和 br0
// ip l s veth0 netns ns1
// ip l s br-veth0 master br0
// ip l s br-veth0 up
//
// ip l s veth1 netns ns2
// ip l s br-veth1 master br0
// ip l s br-veth1 up
//
// 给两个 ns 中的 veth 配置 IP 并启用
// ip netns exec ns1 ip a a 10.1.1.2/24 dev veth0
// ip netns exec ns1 ip l s veth0 up
//
// ip netns exec ns2 ip a a 10.1.1.3/24 dev veth1
// ip netns exec ns2 ip l s veth1 up

// SetupVeth
// netns: pod namespace
// ifName: pod interface name
// podIP: pod ip
// br: bridge
// gateway: bridge ip
func SetupVeth(netns ns.NetNS, br netlink.Link, mtu int, ifName string, podIP *net.IPNet, gateway net.IP) error {
	hostIface := &current.Interface{}
	err := netns.Do(func(hostNS ns.NetNS) error {
		// setup lo, kubernetes will call loopback internal
		// loLink, err := netlink.LinkByName("lo")
		// if err != nil {
		// 	return err
		// }

		// if err := netlink.LinkSetUp(loLink); err != nil {
		// 	return err
		// }

		// create the veth pair in the container and move host end into host netns
		hostVeth, containerVeth, err := ip.SetupVeth(ifName, mtu, "", hostNS)
		if err != nil {
			return err
		}
		hostIface.Name = hostVeth.Name

		// set ip for container veth
		conLink, err := netlink.LinkByName(containerVeth.Name)
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(conLink, &netlink.Addr{IPNet: podIP}); err != nil {
			return err
		}

		// setup container veth
		if err := netlink.LinkSetUp(conLink); err != nil {
			return err
		}

		// add default route
		if err := ip.AddDefaultRoute(gateway, conLink); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	// need to lookup hostVeth again as its index has changed during ns move
	hostVeth, err := netlink.LinkByName(hostIface.Name)
	if err != nil {
		return fmt.Errorf("failed to lookup %q: %v", hostIface.Name, err)
	}

	if hostVeth == nil {
		return fmt.Errorf("nil hostveth")
	}

	// connect host veth end to the bridge
	if err := netlink.LinkSetMaster(hostVeth, br); err != nil {
		return fmt.Errorf("failed to connect %q to bridge %v: %v", hostVeth.Attrs().Name, br.Attrs().Name, err)
	}

	return nil
}

func DelVeth(netns ns.NetNS, ifName string) error {
	return netns.Do(func(ns.NetNS) error {
		l, err := netlink.LinkByName(ifName)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return netlink.LinkDel(l)
	})
}

func CheckVeth(netns ns.NetNS, ifName string, ip net.IP) error {
	return netns.Do(func(ns.NetNS) error {
		l, err := netlink.LinkByName(ifName)
		if err != nil {
			return err
		}

		ips, err := netlink.AddrList(l, netlink.FAMILY_V4)
		if err != nil {
			return err
		}

		for _, addr := range ips {
			if addr.IP.Equal(ip) {
				return nil
			}
		}

		return fmt.Errorf("failed to find ip %s for %s", ip, ifName)
	})
}

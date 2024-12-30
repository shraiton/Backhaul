package transport

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"time"
)

var fixedBindIP net.IP
var fixCounter int = 0
var NumberOfFixedConnections = 50

// TravorDial handles dialing with a random or fixed IPv6 bind
type TravorDial struct {
	v6subnet string
}

func NewTravorDialer(v6subnet string) *TravorDial {
	return &TravorDial{
		v6subnet: v6subnet,
	}
}

func (d *TravorDial) Dial(network, addr string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("unsupported network: %s", network)
	}

	var err error
	if fixedBindIP == nil {
		fixedBindIP, err = generateIPv6(d.v6subnet, 1)
		if err != nil {
			return nil, err
		}
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %v", err)
	}

	var ip net.IP
	if net.ParseIP(host) == nil {
		ip, err = resolveToIPv6(host)
		if err != nil {
			return nil, err
		}
	} else {
		ip = net.ParseIP(host)
		if ip.To4() != nil {
			return nil, fmt.Errorf("IPv4 addresses are not allowed: %s", ip.String())
		}
	}

	if fixCounter > NumberOfFixedConnections {
		fixCounter = 0
		fixedBindIP, err = generateIPv6(d.v6subnet, 1)
		if err != nil {
			return nil, err
		}
	}

	fullAddr := net.JoinHostPort(ip.String(), port)
	localAddr := &net.TCPAddr{IP: fixedBindIP}
	dialer := net.Dialer{LocalAddr: localAddr}
	fixCounter += 1
	return dialer.Dial("tcp", fullAddr)
}

func (d *TravorDial) GetV6IP() net.IP {
	var err error
	if fixedBindIP == nil {
		fixedBindIP, err = generateIPv6(d.v6subnet, 1)
		if err != nil {
			//fmt.Println("error generating v6:", err.Error())
			return nil
		}
	}

	if fixCounter > NumberOfFixedConnections {
		fixCounter = 0
		fixedBindIP, err = generateIPv6(d.v6subnet, 1)
		if err != nil {
			//fmt.Println("error gen2", err.Error())
			return nil
		}
	}

	fixCounter += 1
	return fixedBindIP
}

func (d *TravorDial) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("unsupported network: %s", network)
	}

	var err error
	if fixedBindIP == nil {
		fixedBindIP, err = generateIPv6(d.v6subnet, 1)
		if err != nil {
			return nil, err
		}
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %v", err)
	}

	var ip net.IP
	if net.ParseIP(host) == nil {
		ip, err = resolveToIPv6(host)
		if err != nil {
			return nil, err
		}
	} else {
		ip = net.ParseIP(host)
		if ip.To4() != nil {
			return nil, fmt.Errorf("IPv4 addresses are not allowed: %s", ip.String())
		}
	}

	if fixCounter > NumberOfFixedConnections {
		fixCounter = 0
		fixedBindIP, err = generateIPv6(d.v6subnet, 1)
		if err != nil {
			return nil, err
		}
	}

	fullAddr := net.JoinHostPort(ip.String(), port)
	localAddr := &net.TCPAddr{IP: fixedBindIP}
	dialer := net.Dialer{
		LocalAddr: localAddr,
	}
	fixCounter += 1

	ct, _ := context.WithTimeout(ctx, 3*time.Second)
	conn, err := dialer.DialContext(ct, "tcp", fullAddr)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func resolveToIPv6(host string) (net.IP, error) {

	addresses, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve hostname: %w", err)
	}

	for _, addr := range addresses {
		if addr.To16() != nil && addr.To4() == nil {
			return addr, nil
		}
	}

	return nil, fmt.Errorf("no IPv6 address found for hostname: %s", host)
}

func generateIPv6(subnet string, count int) (net.IP, error) {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet: %v", err)
	}

	if count <= 0 {
		return nil, fmt.Errorf("count must be greater than 0")
	}

	prefixLen, bits := ipNet.Mask.Size()
	if bits != 128 {
		return nil, fmt.Errorf("only IPv6 subnets are supported")
	}

	// Convert IP to 16-byte form
	ipNet.IP = ipNet.IP.To16()
	if ipNet.IP == nil {
		return nil, fmt.Errorf("invalid IPv6 address")
	}

	// Ensure base IP is the network address
	networkIP := ipNet.IP.Mask(ipNet.Mask)

	// Calculate the number of host bits
	hostBits := 128 - prefixLen

	// Calculate the range of addresses within the subnet
	maxHosts := new(big.Int).Lsh(big.NewInt(1), uint(hostBits))

	var ips []net.IP
	for i := 0; i < count; i++ {
		// Generate a random offset within the range of the subnet
		offset, err := rand.Int(rand.Reader, maxHosts)
		if err != nil {
			return nil, fmt.Errorf("failed to generate random offset: %v", err)
		}

		// Convert the base IP to a big.Int
		baseIP := big.NewInt(0).SetBytes(networkIP)

		// Add the offset to the base IP
		randomIP := big.NewInt(0).Add(baseIP, offset)

		// Ensure the result is 16 bytes (IPv6 size)
		ip := make(net.IP, net.IPv6len)
		randomIP.FillBytes(ip)

		return ip, nil
		//ips = append(ips, ip)
	}

	return ips[0], nil
}

//func main() {
//
//	for {
//
//		dialer := NewCustomDial("2a0b:4140:e1e7::/48")
//
//		// Create a custom dialer
//		// Define a custom HTTP transport with the dialer
//		transport := &http.Transport{
//			Dial: dialer.Dial,
//		}
//
//		// Create an HTTP client with the custom transport
//		client := &http.Client{
//			Transport: transport,
//			Timeout:   15 * time.Second, // Overall timeout for the request
//		}
//
//		// Define the URL to fetch
//		url := "https://ifconfig.me"
//
//		// Perform the HTTP request
//		resp, err := client.Get(url)
//		if err != nil {
//			fmt.Printf("Error making request: %v\n", err)
//			return
//		}
//		defer resp.Body.Close()
//
//		// Read the response body
//		body, err := ioutil.ReadAll(resp.Body)
//		if err != nil {
//			fmt.Printf("Error reading response body: %v\n", err)
//			return
//		}
//
//		// Print the response body as a string
//		fmt.Println(string(body))
//
//		time.Sleep(1 * time.Second)
//	}
//
//}

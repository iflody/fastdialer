package fastdialer

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/projectdiscovery/hmap/store/hybrid"
	retryabledns "github.com/projectdiscovery/retryabledns"
)

// Dialer structure containing data information
type Dialer struct {
	dnsclient     *retryabledns.Client
	hm            *hybrid.HybridMap
	dialerHistory *hybrid.HybridMap
	dialer        *net.Dialer
}

// NewDialer instance
func NewDialer(options Options) (*Dialer, error) {
	dnsclient := retryabledns.New(options.BaseResolvers, options.MaxRetries)
	hm, err := hybrid.New(hybrid.DefaultDiskOptions)
	if err != nil {
		return nil, err
	}
	dialerHistory, err := hybrid.New(hybrid.DefaultDiskOptions)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 10 * time.Second,
		DualStack: true,
	}

	// load hardcoded values from host file
	if options.HostsFile {
		// nolint:errcheck // if they cannot be loaded it's not a hard failure
		loadHostsFile(hm)
	}

	return &Dialer{dnsclient: dnsclient, hm: hm, dialerHistory: dialerHistory, dialer: dialer}, nil
}

// Dial function compatible with net/http
func (d *Dialer) Dial(ctx context.Context, network, address string) (conn net.Conn, err error) {
	separator := strings.LastIndex(address, ":")

	// check if data is in cache
	hostname := address[:separator]
	data, err := d.GetDNSData(hostname)
	if err != nil {
		// otherwise attempt to retrieve it
		data, err = d.dnsclient.Resolve(hostname)
	}

	if err != nil || len(data.A)+len(data.AAAA) == 0 {
		return nil, &NoAddressFoundError{}
	}

	// Dial to the IPs finally.
	for _, ip := range append(data.A, data.AAAA...) {
		conn, err = d.dialer.DialContext(ctx, network, ip+address[separator:])
		if err == nil {
			setErr := d.dialerHistory.Set(hostname, []byte(ip))
			if setErr != nil {
				return nil, err
			}
			break
		}
	}
	return
}

// Close instance and cleanups
func (d *Dialer) Close() {
	d.hm.Close()
	d.dialerHistory.Close()
}

// GetDialedIP returns the ip dialed by the HTTP client
func (d *Dialer) GetDialedIP(hostname string) string {
	v, ok := d.dialerHistory.Get(hostname)
	if ok {
		return string(v)
	}

	return ""
}

// GetDNSDataFromCache cached by the resolver
func (d *Dialer) GetDNSDataFromCache(hostname string) (*retryabledns.DNSData, error) {
	var data retryabledns.DNSData
	dataBytes, ok := d.hm.Get(hostname)
	if !ok {
		return nil, fmt.Errorf("No data found")
	}

	err := data.Unmarshal(dataBytes)
	return &data, err
}

// GetDNSData for the given hostname
func (d *Dialer) GetDNSData(hostname string) (*retryabledns.DNSData, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		if ip.To4() != nil {
			return &retryabledns.DNSData{A: []string{hostname}}, nil
		}
		if ip.To16() != nil {
			return &retryabledns.DNSData{AAAA: []string{hostname}}, nil
		}
	}
	var (
		data *retryabledns.DNSData
		err  error
	)
	data, err = d.GetDNSDataFromCache(hostname)
	if err != nil {
		data, err = d.dnsclient.Resolve(hostname)
		if err != nil {
			return nil, err
		}
		b, _ := data.Marshal()
		err = d.hm.Set(hostname, b)
		if err != nil {
			return nil, err
		}

		return data, nil
	}

	return data, nil
}

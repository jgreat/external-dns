package ad

import (
	"fmt"
	"net"
	"os"

	"github.com/Sirupsen/logrus"
	"github.com/miekg/dns"
	"github.com/rancher/external-dns/providers"
	"github.com/rancher/external-dns/utils"
)

type adProvider struct {
	nameserver string
	zoneName   string
}

func init() {
	providers.RegisterProvider("ad", &adProvider{})
}

func (r *adProvider) Init(rootDomainName string) error {
	var host, port string
	if host = os.Getenv("AD_HOST"); len(host) == 0 {
		return fmt.Errorf("AD_HOST is not set")
	}

	if port = os.Getenv("AD_PORT"); len(port) == 0 {
		port = "53"
	}

	r.nameserver = net.JoinHostPort(host, port)
	r.zoneName = dns.Fqdn(rootDomainName)

	logrus.Infof("Configured %s with zone '%s' and nameserver '%s'",
		r.GetName(), r.zoneName, r.nameserver)

	return nil
}

func (*adProvider) GetName() string {
	return "AD"
}

func (r *adProvider) HealthCheck() error {
	_, err := r.GetRecords()
	return err
}

func (r *adProvider) AddRecord(record utils.DnsRecord) error {
	logrus.Debugf("Adding RRset '%s %s'", record.Fqdn, record.Type)
	m := new(dns.Msg)
	m.SetUpdate(r.zoneName)
	var rrs []dns.RR
	for _, rec := range record.Records {
		logrus.Debugf("Adding RR: '%s %d %s %s'", record.Fqdn, record.TTL, record.Type, rec)
		rr, err := dns.NewRR(fmt.Sprintf("%s %d %s %s", record.Fqdn, record.TTL, record.Type, rec))
		if err != nil {
			return fmt.Errorf("Failed to build RR: %v", err)
		}
		rrs = append(rrs, rr)
	}

	m.Insert(rrs)
	err := r.sendMessage(m)
	if err != nil {
		return fmt.Errorf("AD query failed: %v", err)
	}

	return nil
}

func (r *adProvider) RemoveRecord(record utils.DnsRecord) error {
	logrus.Debugf("Removing RRset '%s %s'", record.Fqdn, record.Type)
	m := new(dns.Msg)
	m.SetUpdate(r.zoneName)
	rr, err := dns.NewRR(fmt.Sprintf("%s 0 %s 0.0.0.0", record.Fqdn, record.Type))
	if err != nil {
		return fmt.Errorf("Could not construct RR: %v", err)
	}

	rrs := make([]dns.RR, 1)
	rrs[0] = rr
	m.RemoveRRset(rrs)
	err = r.sendMessage(m)
	if err != nil {
		return fmt.Errorf("AD query failed: %v", err)
	}

	return nil
}

func (r *adProvider) UpdateRecord(record utils.DnsRecord) error {
	err := r.RemoveRecord(record)
	if err != nil {
		return err
	}

	return r.AddRecord(record)
}

func (r *adProvider) GetRecords() ([]utils.DnsRecord, error) {
	var records []utils.DnsRecord
	list, err := r.list()
	if err != nil {
		return records, err
	}

OuterLoop:
	for _, rr := range list {
		if rr.Header().Class != dns.ClassINET {
			continue
		}

		rrFqdn := rr.Header().Name
		rrTTL := int(rr.Header().Ttl)
		var rrType string
		var rrValues []string
		switch rr.Header().Rrtype {
		case dns.TypeCNAME:
			rrValues = []string{rr.(*dns.CNAME).Target}
			rrType = "CNAME"
		case dns.TypeA:
			rrValues = []string{rr.(*dns.A).A.String()}
			rrType = "A"
		case dns.TypeAAAA:
			rrValues = []string{rr.(*dns.AAAA).AAAA.String()}
			rrType = "AAAA"
		case dns.TypeTXT:
			rrValues = rr.(*dns.TXT).Txt
			rrType = "TXT"
		default:
			continue // Unhandled record type
		}

		for idx, existingRecord := range records {
			if existingRecord.Fqdn == rrFqdn && existingRecord.Type == rrType {
				records[idx].Records = append(records[idx].Records, rrValues...)
				continue OuterLoop
			}
		}

		record := utils.DnsRecord{
			Fqdn:    rrFqdn,
			Type:    rrType,
			TTL:     rrTTL,
			Records: rrValues,
		}

		records = append(records, record)
	}

	return records, nil
}

func (r *adProvider) sendMessage(msg *dns.Msg) error {
	c := new(dns.Client)
	c.SingleInflight = true
	resp, _, err := c.Exchange(msg, r.nameserver)
	if err != nil {
		return err
	}

	if resp != nil && resp.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("Bad return code: %s", dns.RcodeToString[resp.Rcode])
	}

	return nil
}

func (r *adProvider) list() ([]dns.RR, error) {
	logrus.Debugf("Fetching records for '%s'", r.zoneName)
	t := new(dns.Transfer)

	m := new(dns.Msg)
	m.SetAxfr(r.zoneName)

	env, err := t.In(m, r.nameserver)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch records via AXFR: %v", err)
	}

	var records []dns.RR
	for e := range env {
		if e.Error != nil {
			logrus.Errorf("AXFR envelope error: %v", e.Error)
			continue
		}
		records = append(records, e.RR...)
	}

	return records, nil
}
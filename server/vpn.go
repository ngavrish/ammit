package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// The product's corporate names (Aurora, Cognito, the git server) resolve only
// over a VPN that logs out on its own. When they stop resolving the runner
// does not repair anything: it says so in one line (agent-runner
// agent_runner.py, "product DNS outage (<hosts>) during <phase>; waiting for
// it"), waits runner.product_dns_grace_seconds for the names to come back and
// ends the run as an outage if they do not. That wait emits no progress event,
// so limits.wait closes the run as `waiting` first - and a fix run was queued
// for a VPN no code brings back. Chain 66d7f44a, 2026-11-13: run 6ef8217b
// (APF-3313) waited in mapmerge on
// adi-seir-data-auroracluster-read-write.prod.hulu.com from 13:12:30 to
// 14:12:31, was closed as `waiting`, and cycle 77 was opened on it; the same
// name had resolved for the same ticket the day before.
const productDNSLine = "product DNS outage ("

// productDNSDown is a drop whose runner said the product's names stopped
// resolving and whose names still do not resolve from here. Only names under a
// real (ICANN) public suffix count: the runner's host scan has named phrases
// like "mysql.connector.connect" and "1.4.locating" before, and a wait on a
// name that never existed is the runner's defect, which a fix cycle is for.
func productDNSDown(d *dropFact) bool {
	hosts := productDNSHosts(d.run)
	if len(hosts) == 0 {
		return false
	}
	for _, h := range hosts {
		if !corporateName(h) || !notFound(h) {
			return false
		}
	}
	return true
}

// productDNSHosts reads the hosts of the runner's last product DNS outage line
// for this run.
func productDNSHosts(run string) []string {
	var text string
	mu.Lock()
	err := db.QueryRow(`SELECT coalesce(payload->>'text','') FROM events
	             WHERE run=? AND kind='service_log' AND payload->>'text' LIKE ?
	             ORDER BY id DESC LIMIT 1`, run, productDNSLine+"%").Scan(&text)
	mu.Unlock()
	if err != nil {
		return nil
	}
	return dnsLineHosts(text)
}

// dnsLineHosts is the host list between the line's parentheses.
func dnsLineHosts(text string) []string {
	_, rest, ok := strings.Cut(text, productDNSLine)
	if !ok {
		return nil
	}
	list, _, ok := strings.Cut(rest, ")")
	if !ok {
		return nil
	}
	parts := strings.Split(list, ",")
	hosts := make([]string, 0, len(parts))
	for _, h := range parts {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// corporateName is whether host sits under an ICANN public suffix with a
// registrable domain of its own.
func corporateName(host string) bool {
	suffix, icann := publicsuffix.PublicSuffix(host)
	return icann && suffix != host && strings.Contains(host, ".")
}

// notFound is whether the resolver says this name does not exist now. A
// resolver that cannot be asked proves nothing either way.
func notFound(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := net.DefaultResolver.LookupHost(ctx, host)
	var dnsErr *net.DNSError
	return err != nil && errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

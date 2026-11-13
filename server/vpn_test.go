package main

import (
	"reflect"
	"testing"
)

// Run 6ef8217b (APF-3313), 2026-11-13 13:12:30: the runner's line the
// watchdog reads to tell a VPN outage from a stall.
func TestDNSLineHosts(t *testing.T) {
	line := "product DNS outage (adi-seir-data-auroracluster-read-write.prod.hulu.com) during mapmerge; waiting for it"
	want := []string{"adi-seir-data-auroracluster-read-write.prod.hulu.com"}
	if got := dnsLineHosts(line); !reflect.DeepEqual(got, want) {
		t.Fatalf("dnsLineHosts = %v, want %v", got, want)
	}
	two := "product DNS outage (a.prod.hulu.com, cognito-idp.us-east-1.amazonaws.com) during gatefix [claim-3]; waiting for it"
	if got := dnsLineHosts(two); len(got) != 2 || got[1] != "cognito-idp.us-east-1.amazonaws.com" {
		t.Fatalf("dnsLineHosts(two) = %v", got)
	}
	if got := dnsLineHosts("control: retry session mapmerge"); got != nil {
		t.Fatalf("a line that is not the runner's outage named hosts: %v", got)
	}
}

// The runner's host scan named these on runs bef2360d and d7123ca0; none is a
// name a VPN serves, so a wait on one is the runner's defect and goes to a fix.
func TestCorporateName(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"adi-seir-data-auroracluster-read-write.prod.hulu.com", true},
		{"cognito-idp.us-east-1.amazonaws.com", true},
		{"mysql.connector.connect", false},
		{"1.4.locating", false},
		{"com", false},
	}
	for _, c := range cases {
		if got := corporateName(c.host); got != c.want {
			t.Errorf("corporateName(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/spf13/pflag"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	dnspod "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/dnspod/v20210323"
)

var ips = []string{}

var secretId = ""
var secretKey = ""
var testHost = ""
var subdomains = []string{}
var domain = ""
var line = ""
var dryRun = false
var ttl uint64 = 60
var checkInterval = 30 * time.Second
var remark = "Health check update"

func init() {
	pflag.StringSliceVar(&ips, "ip", ips, "IPs to check")
	pflag.StringVar(&secretId, "secret-id", secretId, "Tencent Cloud API Secret ID")
	pflag.StringVar(&secretKey, "secret-key", secretKey, "Tencent Cloud API Secret Key")
	pflag.StringVar(&testHost, "check-host", testHost, "Host to test connectivity against")
	pflag.StringSliceVar(&subdomains, "subdomains", subdomains, "Subdomains to manage")
	pflag.StringVar(&domain, "domain", domain, "Domain to manage (e.g., 'example.com')")
	pflag.StringVar(&line, "line", line, "DNS record line (e.g., 'Default')")
	pflag.BoolVar(&dryRun, "dry-run", dryRun, "If set, will not make any changes to DNS records")
	pflag.Uint64Var(&ttl, "ttl", ttl, "TTL for DNS records (in seconds)")
	pflag.StringVar(&remark, "remark", remark, "Remark for DNS records")
	pflag.DurationVar(&checkInterval, "check-interval", checkInterval, "Interval between health checks (e.g., '30s', '1m')")
	pflag.Parse()
}

func main() {
	if secretId == "" || secretKey == "" || testHost == "" || len(subdomains) == 0 || domain == "" {
		log.Fatal("Required parameters are missing. Please provide --secret-id, --secret-key, --check-host, --subdomains, and --domain.")
	}
	client, err := dnspod.NewClientWithSecretId(
		secretId, secretKey, "")
	if err != nil {
		fmt.Printf("Failed to create DNSPod client: %s", err.Error())
		return
	}

	run(context.Background(), client)
	ticker := time.NewTicker(checkInterval + randomDuration(checkInterval/10))
	defer ticker.Stop()
	for range ticker.C {
		ticker.Reset(checkInterval + randomDuration(checkInterval/10))
		run(context.Background(), client)
	}
}

func randomDuration(max time.Duration) time.Duration {
	return time.Duration(rand.Int63n(int64(max)))
}

func run(ctx context.Context, client *dnspod.Client) {
	aliveIPs, deadIPs := testIPs(ctx, testHost, ips)

	for _, subdomain := range subdomains {
		err := checkAndUpdateDNS(ctx, client, aliveIPs, deadIPs, subdomain, domain, line, ttl)
		if err != nil {
			fmt.Printf("Check and update DNS err: %s", err.Error())
			return
		}
	}
}

func checkAndUpdateDNS(ctx context.Context, client *dnspod.Client, aliveIPs, deadIPs []string, subdomain, domain, recordLine string, ttl uint64) error {
	describeRecordListResponse, err := client.DescribeRecordListWithContext(ctx, &dnspod.DescribeRecordListRequest{
		Domain:    new(domain),
		Subdomain: new(subdomain),
	})

	var records []*dnspod.RecordListItem
	if err != nil {
		if tcerror, ok := err.(*tcerrors.TencentCloudSDKError); ok {
			if tcerror.Code != "ResourceNotFound.NoDataOfRecord" {
				return fmt.Errorf("failed to describe record list: %w", err)
			}
		} else {
			return fmt.Errorf("failed to describe record list: %w", err)
		}
	} else {
		records = describeRecordListResponse.Response.RecordList
	}

	handleRecords := []*dnspod.RecordListItem{}
	for _, record := range records {
		if record.Type != nil && *record.Type != "A" {
			continue
		}

		// log.Printf("Existing record: %s", formatRecord(record))
		handleRecords = append(handleRecords, record)
	}

	var updated bool

	for _, ip := range aliveIPs {
		found := false
		var record *dnspod.RecordListItem
		for _, r := range handleRecords {
			if r.Value != nil && *r.Value == ip {
				found = true
				record = r
				break
			}
		}

		if !found {
			updated = true
			if dryRun {
				log.Printf("Dry run: would add DNS record for IP: %s, Subdomain: %s", ip, subdomain)
				continue
			}
			// Add DNS record
			log.Printf("Add DNS record for IP: %s, Subdomain: %s", ip, subdomain)
			_, err := client.CreateRecordWithContext(ctx, &dnspod.CreateRecordRequest{
				Domain:     new(domain),
				RecordType: new("A"),
				RecordLine: new(recordLine),
				SubDomain:  new(subdomain),
				Value:      new(ip),
				TTL:        new(ttl),
				Remark:     new(remark),
			})
			if err != nil {
				log.Printf("Failed to create record for %s: %s", ip, err.Error())
			}
		} else {
			// check difference in line or ttl
			diffs := diffRecords(record, &dnspod.RecordListItem{
				RecordId: record.RecordId,
				Type:     new("A"),
				Line:     new(recordLine),
				Value:    new(ip),
				TTL:      new(ttl),
				Remark:   new(remark),
				Status:   new("ENABLE"),
			})

			if len(diffs) != 0 {
				updated = true
				if dryRun {
					log.Printf("Dry run: would modify DNS record for IP: %s, Subdomain: %s, Differences: %v", ip, subdomain, diffs)
					continue
				}
				// Modify DNS record
				log.Printf("Modify DNS record for IP: %s, Subdomain: %s, Differences: %v", ip, subdomain, diffs)
				_, err := client.ModifyRecordWithContext(ctx, &dnspod.ModifyRecordRequest{
					Domain:     new(domain),
					RecordId:   record.RecordId,
					RecordType: new("A"),
					RecordLine: new(recordLine),
					SubDomain:  new(subdomain),
					Value:      new(ip),
					TTL:        new(ttl),
					Remark:     new(remark),
					Status:     new("ENABLE"),
				})
				if err != nil {
					log.Printf("Failed to modify record for %s: %s", ip, err.Error())
				}
			}
		}
	}

	for _, ip := range deadIPs {
		found := false
		var record *dnspod.RecordListItem

		for _, r := range handleRecords {
			if r.Value != nil && *r.Value == ip {
				found = true
				record = r
				break
			}
		}

		if found {
			diffs := diffRecords(record, &dnspod.RecordListItem{
				RecordId: record.RecordId,
				Type:     new("A"),
				Line:     new(recordLine),
				Value:    new(ip),
				TTL:      new(ttl),
				Remark:   new(remark),
				Status:   new("DISABLE"),
			})

			if len(diffs) != 0 {
				updated = true
				// Remove DNS record
				if dryRun {
					log.Printf("Dry run: would remove DNS record for IP: %s, Subdomain: %s, Differences: %v", ip, subdomain, diffs)
					continue
				}

				log.Printf("Remove DNS record for IP: %s, Subdomain: %s, Differences: %v", ip, subdomain, diffs)
				_, err := client.ModifyRecordWithContext(ctx, &dnspod.ModifyRecordRequest{
					Domain:     new(domain),
					RecordId:   record.RecordId,
					RecordType: new("A"),
					RecordLine: new(recordLine),
					SubDomain:  new(subdomain),
					Value:      new(ip),
					TTL:        new(ttl),
					Remark:     new(remark),
					Status:     new("DISABLE"),
				})
				if err != nil {
					log.Printf("Failed to delete record for %s: %s", ip, err.Error())
				}
			}
		}
	}

	if !updated {
		log.Printf("No DNS records need to be updated for Subdomain: %s", subdomain)
	}

	return nil
}

func formatRecord(record *dnspod.RecordListItem) string {
	return fmt.Sprintf("ID=%d, Type=%s, Value=%s, SubDomain=%s, Status=%s, Line=%s, Remark=%s", *record.RecordId, *record.Type, *record.Value, *record.Name, *record.Status, *record.Line, *record.Remark)
}

func diffRecords(existing, desired *dnspod.RecordListItem) []string {
	var diffs []string
	if existing.Type != nil && desired.Type != nil && *existing.Type != *desired.Type {
		diffs = append(diffs, fmt.Sprintf("Type: %s -> %s", *existing.Type, *desired.Type))
	}
	if existing.Value != nil && desired.Value != nil && *existing.Value != *desired.Value {
		diffs = append(diffs, fmt.Sprintf("Value: %s -> %s", *existing.Value, *desired.Value))
	}
	if existing.Line != nil && desired.Line != nil && *existing.Line != *desired.Line {
		diffs = append(diffs, fmt.Sprintf("Line: %s -> %s", *existing.Line, *desired.Line))
	}
	if existing.TTL != nil && desired.TTL != nil && *existing.TTL != *desired.TTL {
		diffs = append(diffs, fmt.Sprintf("TTL: %d -> %d", *existing.TTL, *desired.TTL))
	}
	if existing.Status != nil && desired.Status != nil && *existing.Status != *desired.Status {
		diffs = append(diffs, fmt.Sprintf("Status: %s -> %s", *existing.Status, *desired.Status))
	}
	if existing.Remark != nil && desired.Remark != nil && *existing.Remark != *desired.Remark {
		diffs = append(diffs, fmt.Sprintf("Remark: %s -> %s", *existing.Remark, *desired.Remark))
	}
	return diffs
}

func testIPs(ctx context.Context, host string, ips []string) ([]string, []string) {
	var aliveIPs []string
	var deadIPs []string
	var mut sync.Mutex
	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Go(func() {
			ok := concurrentIPAlive(ctx, host, ip)
			mut.Lock()
			defer mut.Unlock()
			if ok {
				aliveIPs = append(aliveIPs, ip)
			} else {
				deadIPs = append(deadIPs, ip)
			}
		})
	}
	wg.Wait()
	if len(aliveIPs) == 0 {
		return ips, []string{}
	}
	return aliveIPs, deadIPs
}

var keepFailed sync.Map

func concurrentIPAlive(ctx context.Context, host, ip string) bool {
	keepFailedValue, ok := keepFailed.Load(ip)
	if ok {
		if t, ok := keepFailedValue.(time.Time); ok {
			if time.Since(t) < 5*time.Minute {
				log.Printf("IP %s is recently failed, skipping check", ip)
				return false
			} else {
				keepFailed.Delete(ip)
			}
		}
	}
	const concurrency = 3
	errCh := make(chan error, concurrency)

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for range concurrency {
		go func() {
			if err := isIPAlive(ctx, host, ip); err != nil {
				errCh <- err
			} else {
				errCh <- nil
			}
		}()
	}

	for range concurrency {
		if err := <-errCh; err != nil {
			log.Printf("IP %s is not alive: %s", ip, err.Error())
			keepFailed.Store(ip, time.Now())
			return false
		}
	}
	return true
}

func isIPAlive(ctx context.Context, host, ip string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+host+"/", nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "HealthRouter/0.x")

	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, ip+":443")
			},
		},
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	return nil
}

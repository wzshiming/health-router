package main

import (
	"fmt"

	dnspod "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/dnspod/v20210323"
)

// recordShape names the managed line (Line) and the TTL/Remark its records are normalized to.
type recordShape struct {
	Line   string
	TTL    uint64
	Remark string
}

type changeReason int

const (
	reasonAlive     changeReason = iota // configured & alive → shape+ENABLE
	reasonDead                          // configured & not alive → shape+DISABLE
	reasonUnmanaged                     // Value not configured (record still on the managed line) → shape+DISABLE
	reasonDuplicate                     // extra record of a Value → Status DISABLE only
)

type recordChange struct {
	Record  *dnspod.RecordListItem // remote record, untouched
	Desired *dnspod.RecordListItem // freshly allocated; RecordId, Value, Line and LineId as Record, Type "A"
	Reason  changeReason
	Diffs   []string // diffRecords(Record, Desired), never empty
}

type reconcilePlan struct {
	Creates []string       // alive order, each IP at most once
	Changes []recordChange // remote record order
}

// reconcile considers only records on the managed line: it normalizes the first one
// per Value and only disables its duplicates; records on other lines are never touched.
func reconcile(records []*dnspod.RecordListItem, configured, alive []string, shape recordShape) reconcilePlan {
	configuredSet := ipSet(configured)
	aliveSet := ipSet(alive)

	primary := make(map[string]int)
	for i, r := range records {
		if !isManaged(r, shape.Line) {
			continue
		}
		if _, ok := primary[*r.Value]; !ok {
			primary[*r.Value] = i
		}
	}

	var plan reconcilePlan
	seen := make(map[string]bool)
	for _, ip := range alive {
		if seen[ip] {
			continue
		}
		seen[ip] = true
		if _, exists := primary[ip]; !exists {
			plan.Creates = append(plan.Creates, ip)
		}
	}

	for i, r := range records {
		if !isManaged(r, shape.Line) {
			continue
		}
		var desired *dnspod.RecordListItem
		var reason changeReason
		if primary[*r.Value] == i {
			reason = classifyValue(*r.Value, configuredSet, aliveSet)
			desired = &dnspod.RecordListItem{
				RecordId: r.RecordId,
				Type:     new("A"),
				Value:    r.Value,
				Line:     r.Line,
				LineId:   r.LineId,
				TTL:      new(shape.TTL),
				Remark:   new(shape.Remark),
				Status:   new(statusForReason(reason)),
			}
		} else {
			reason = reasonDuplicate
			desired = &dnspod.RecordListItem{
				RecordId: r.RecordId,
				Type:     new("A"),
				Value:    r.Value,
				Line:     r.Line,
				LineId:   r.LineId,
				TTL:      r.TTL,
				Remark:   r.Remark,
				Status:   new("DISABLE"),
			}
		}
		diffs := diffRecords(r, desired)
		if len(diffs) == 0 {
			continue
		}
		plan.Changes = append(plan.Changes, recordChange{Record: r, Desired: desired, Reason: reason, Diffs: diffs})
	}
	return plan
}

func ipSet(ips []string) map[string]bool {
	set := make(map[string]bool, len(ips))
	for _, ip := range ips {
		set[ip] = true
	}
	return set
}

// isHandleable reports whether r is an A record that can be matched by Value and modified by id.
func isHandleable(r *dnspod.RecordListItem) bool {
	return r != nil && (r.Type == nil || *r.Type == "A") && r.Value != nil && r.RecordId != nil
}

// isManaged reports whether r is a handleable A record on the managed line.
func isManaged(r *dnspod.RecordListItem, line string) bool {
	return isHandleable(r) && r.Line != nil && *r.Line == line
}

func classifyValue(value string, configured, alive map[string]bool) changeReason {
	switch {
	case alive[value]:
		return reasonAlive
	case configured[value]:
		return reasonDead
	default:
		return reasonUnmanaged
	}
}

func statusForReason(reason changeReason) string {
	if reason == reasonAlive {
		return "ENABLE"
	}
	return "DISABLE"
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

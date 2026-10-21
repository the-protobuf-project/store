// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package dtzone names the companion column that keeps a google.type.DateTime's
// zone.
//
// A DateTime is a civil time plus how to read it: an IANA zone, a fixed UTC
// offset, or nothing (floating). Its column stores the instant it denotes, a
// TIMESTAMPTZ, so ordering, ranges and filters work on time itself; but the
// instant alone forgets the zone, and a weekly 10:00 in Europe/Madrid read
// back as UTC lands at 11:00 local once the clocks change. So every DateTime
// column gets a nullable text companion, "<column>_zone", holding the zone the
// value was written in:
//
//	"Europe/Madrid"   an IANA zone
//	"+05:30"          a fixed UTC offset
//	"floating"        neither: a wall-clock time read in whatever zone applies
//
// NULL is a row written before the companion existed, read back as it always
// was: the instant, in UTC.
package dtzone

import "github.com/the-protobuf-project/protokit/schema"

// Suffix names the companion: "start_date_time" -> "start_date_time_zone".
const Suffix = "_zone"

// Is reports whether c stores a google.type.DateTime the caller writes (audit
// columns are server time and need no zone).
func Is(c *schema.Column) bool {
	return c.Type == schema.TypeTimestamp && !c.AutoCreate && !c.AutoUpdate &&
		c.Source != nil && c.Source.Message() != nil &&
		c.Source.Message().FullName() == "google.type.DateTime"
}

// Name is the companion column name of DateTime column c.
func Name(c *schema.Column) string { return c.Name + Suffix }

// Of returns the companion of DateTime column c in t, or nil when t has none.
func Of(t *schema.Table, c *schema.Column) *schema.Column {
	name := Name(c)
	for _, x := range t.Columns {
		if x.Name == name && x.Source == nil {
			return x
		}
	}
	return nil
}

// Add synthesizes the companion after every DateTime column of t that lacks
// one. A proto field already named like the companion is left alone, and its
// DateTime keeps the instant-only reading.
func Add(t *schema.Table) {
	var out []*schema.Column
	for _, c := range t.Columns {
		out = append(out, c)
		if !Is(c) {
			continue
		}
		taken := false
		for _, x := range t.Columns {
			taken = taken || x.Name == Name(c)
		}
		if taken {
			continue
		}
		out = append(out, &schema.Column{
			Name:     Name(c),
			Comment:  "Zone " + c.Name + " was written in: an IANA zone, a \"+hh:mm\" offset, or \"floating\".",
			Type:     schema.TypeString,
			Optional: true,
		})
	}
	t.Columns = out
}

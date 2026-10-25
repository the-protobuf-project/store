// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// EnumValuePrefix is the prefix the proto values of c's enum carry ("STATE_"
// for STATE_ACTIVE), which the stored value drops. It comes from the proto
// enum's simple name, not from the IR enum's LocalName: two enums sharing a
// simple name in one schema are renamed apart (Webhook.State → WebhookState)
// while their values keep the proto's own "STATE_" prefix. A column with no
// proto source falls back to LocalName, the simple name everywhere else.
func EnumValuePrefix(c *schema.Column) string {
	name := c.Enum.LocalName
	if c.Source != nil && c.Source.Enum() != nil {
		name = string(c.Source.Enum().Name())
	}
	return naming.ScreamingSnake(name) + "_"
}

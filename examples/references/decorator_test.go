// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// decorator_test.go is the Shelf store decorator TestStoreDecorator puts
// under the repositories, in a file of its own: it names the gorm models
// package, which shares its name with the repository package.

import (
	"context"

	"gorm.io/gorm"

	"github.com/the-protobuf-project/store/examples/generated/go/gorm/references/catalog/catalog"
)

// countingShelves counts the reads by id it serves.
type countingShelves struct {
	catalog.ShelfStoreIface
	gets *int
}

func (c countingShelves) GetByID(ctx context.Context, id string) (*catalog.Shelf, error) {
	*c.gets++
	return c.ShelfStoreIface.GetByID(ctx, id)
}

// countingShelfStore builds a countingShelves over each handle, counting the
// stores it builds in built.
func countingShelfStore(gets, built *int) func(db *gorm.DB) catalog.ShelfStoreIface {
	return func(db *gorm.DB) catalog.ShelfStoreIface {
		*built++
		return countingShelves{catalog.NewShelfStore(db), gets}
	}
}

package types

// provider.go is store's datasource-provider model: the backend a datasource
// targets and the Prisma-specific projections of it. protokit carries its own
// generic Provider for grouping/validation during the build; store keeps its own
// here so its type package is self-contained (the two never cross — the IR passes
// the provider as a plain string on schema.Database.Provider).

import "fmt"

// Provider identifies the database backend a datasource targets.
type Provider string

const (
	Postgres Provider = "postgres"
	MongoDB  Provider = "mongodb"
	// EVM marks a datasource whose backend is an EVM chain rather than a SQL
	// database; the relational targets reject it.
	EVM Provider = "evm"
)

// ParseProvider normalizes a datasource provider string. Empty means Postgres.
func ParseProvider(s string) (Provider, error) {
	switch s {
	case "", "postgres", "postgresql":
		return Postgres, nil
	case "mongodb", "mongo":
		return MongoDB, nil
	case "evm", "ethereum":
		return EVM, nil
	default:
		return "", fmt.Errorf("unknown datasource provider %q (want postgres, mongodb, or evm)", s)
	}
}

// PrismaProvider is the provider string written into a Prisma datasource block.
func (p Provider) PrismaProvider() string {
	if p == MongoDB {
		return "mongodb"
	}
	return "postgresql"
}

// FragmentExt is the sub-extension used in generated fragment file names:
// <domain>.postgres.prisma or <domain>.mongodb.prisma.
func (p Provider) FragmentExt() string { return string(p) }

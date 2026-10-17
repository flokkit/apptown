// Package adoption owns a project's recorded ways of working: revisions,
// guidance imports, installed policies, lanes, extensions and pack memberships.
// Service is constructed once and exposes operations over private stores.
// Its catalog observations come through Ways; capability observations retain
// their transaction locks. Named queries live in its private adoptionsql package.
package adoption

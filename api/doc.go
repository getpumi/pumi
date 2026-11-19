// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package api is the root of the getpumi.dev/pumi/api module, which holds the
// protobuf schema, the generated Connect code and the schemas for event types
// and facts. Adapters in other repositories import this module without the
// rest of the core.
//
// Import rule: the api module imports the standard library and its own
// third-party dependencies only. It never imports anything from the core
// module getpumi.dev/pumi.
package api

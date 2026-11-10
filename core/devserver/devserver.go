// Package devserver says whether this build may send model requests
// anywhere but caveira.app: a local model server (--base-url, --api-key,
// CAVEIRA_BASE_URL, CAVEIRA_API_KEY, base_url and api_key in config.json or
// a .env file) or another caveira site (CAVEIRA_CLOUD_URL).
//
// Only a build made with -tags devserver can, for tests and for working on
// caveira itself. Every build caveira ships is made without it, so each of
// those is ignored there and the flags don't exist: a caveira account is the
// only way to run it.
package devserver

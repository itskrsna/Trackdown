# Getting started

## Install

Build from source (until pre-built binaries are published):

```bash
git clone <repo-url>
cd trackdown
go build -o trackdown ./cmd/trackdown
```

## Run the server

The management API and web UI require an admin password (see [Configuration](configuration.md)):

```bash
export TRACKDOWN_ADMIN_PASSWORD="a strong password"
./trackdown serve
```

By default this starts the HTTP server on `:8080` with a local SQLite database file for storage. For local experimentation only, `-insecure-no-auth` skips the password requirement entirely — never use this on a server reachable by anyone else.

## Create your first event

Point any Sentry SDK at Trackdown by using a DSN in this shape:

```
http://<public-key>@localhost:8080/<project-id>
```

**If you're using the JavaScript or Python SDK, `project-id` must be numeric** (e.g. `12345`) — see [Compatibility](compatibility.md) for why. Go's SDK has no such restriction. See [SDK setup](sdk-setup/) for language-specific instructions. Once configured, trigger an error in your app — it should appear within moments.

## View it in the dashboard

Open `http://localhost:8080/` in a browser and log in with the admin credentials above. The project you just created appears automatically (projects are provisioned the instant their DSN is first used — there's no separate setup step). From there:

- Click into the project to see its **issues** (errors are automatically grouped — repeated occurrences of the same bug increment one issue's count instead of flooding the list)
- Click an issue to see every event linked to it, resolve/ignore/reopen it
- Click an event to see its full exception chain, stack trace (with your own code visually distinguished from library frames), breadcrumbs, tags, and the raw JSON payload
- Visit a project's **setup** page for ready-to-paste DSN snippets per language

## Next steps

- [Self-hosting](self-hosting.md) — running Trackdown as a persistent service
- [Configuration](configuration.md) — all available flags and environment variables
- [Compatibility matrix](compatibility.md) — which Sentry SDK features are currently supported

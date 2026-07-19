# SDK setup

Per-language guides for pointing an existing Sentry SDK at Trackdown. All five verified SDKs work with the same one-line change: point the DSN at your Trackdown server instead of Sentry's.

```
http://<public_key>@<your-trackdown-host>/<project_id>
```

`public_key` can be anything — Trackdown creates the project automatically on first use with whatever key you choose. **If you're using the JavaScript, Python, .NET, or Java SDK, `project_id` must be numeric** (e.g. `12345`) — those SDKs validate the DSN client-side and reject a non-numeric project ID before ever making a request. Go's SDK has no such restriction. See `docs/compatibility.md` for the full detail and why this is a client-side SDK constraint, not something Trackdown itself imposes.

## Go (`sentry-go`)

```go
sentry.Init(sentry.ClientOptions{
    Dsn: "http://public@your-trackdown-host:8080/myproject",
})
```

## Node.js (`@sentry/node`)

```js
Sentry.init({
  dsn: "http://public@your-trackdown-host:8080/12345", // numeric project ID
});
```

## Python (`sentry-sdk`)

```python
sentry_sdk.init(
    dsn="http://public@your-trackdown-host:8080/12345",  # numeric project ID
)
```

## .NET (`Sentry`)

```csharp
SentrySdk.Init(o =>
{
    o.Dsn = "http://public@your-trackdown-host:8080/12345"; // numeric project ID
});
```

## Java (`io.sentry:sentry`)

```java
Sentry.init(options -> {
    options.setDsn("http://public@your-trackdown-host:8080/12345"); // numeric project ID
});
```

Verification status for every SDK lives in `docs/compatibility.md` — check there before assuming a language works.

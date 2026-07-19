// Sends a real Sentry .NET SDK event to an already-running Trackdown server,
// for manual end-to-end verification against the actual compiled binary
// (not just an in-process test server). Mirrors tools/e2echeck (Go).
// Usage: dotnet run -- <host:port>
using Sentry;

if (args.Length < 1)
{
    Console.Error.WriteLine("usage: dotnet run -- <host:port>");
    Environment.Exit(2);
}
var host = args[0];

// A numeric project ID: @sentry/node and sentry-sdk (Python) both require
// this client-side (confirmed against real "Invalid projectId"-style
// errors); matching that constraint here too rather than assuming .NET is
// more lenient without checking.
const string projectId = "919191";

using (SentrySdk.Init(o =>
{
    o.Dsn = $"http://public@{host}/{projectId}";
    o.TracesSampleRate = 0;
    o.Debug = false;
    o.AutoSessionTracking = false;
    o.SendClientReports = false;
}))
{
    Guid eventId;
    try
    {
        throw new Exception("manual e2e check from Sentry .NET SDK");
    }
    catch (Exception e)
    {
        eventId = SentrySdk.CaptureException(e);
    }
    SentrySdk.Flush(TimeSpan.FromSeconds(2));
    Console.WriteLine($"sent event_id={eventId} to project {projectId} on {host}");
}

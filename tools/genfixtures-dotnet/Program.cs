// Captures real envelope bytes emitted by the Sentry .NET SDK and writes them
// to every Go package's testdata/envelopes/ that needs them (mirrors
// tools/genfixtures-node's behavior for @sentry/node). Run with
// `dotnet run` in this directory. Not part of the shipped product — dev
// tooling only, rerun after any Sentry .NET SDK upgrade to catch wire format
// drift.
using System.IO.Compression;
using System.Net;
using Sentry;

string[] outDirs =
[
    Path.Combine("..", "..", "internal", "protocol", "testdata", "envelopes"),
    Path.Combine("..", "..", "internal", "store", "testdata", "envelopes"),
    Path.Combine("..", "..", "internal", "ingest", "testdata", "envelopes"),
    Path.Combine("..", "..", "internal", "grouping", "testdata", "envelopes"),
];

foreach (var dir in outDirs)
{
    Directory.CreateDirectory(dir);
}

void WriteFixture(string name, byte[] body, string? contentEncoding)
{
    foreach (var dir in outDirs)
    {
        var outPath = Path.Combine(dir, name);
        File.WriteAllBytes(outPath, body);
        Console.WriteLine($"wrote {outPath} ({body.Length} bytes, content-encoding={contentEncoding})");
    }

    // Also write a decompressed sibling (mirrors genfixtures-python's
    // convention) so internal/protocol's parser-level tests can exercise
    // real envelope bytes directly without needing to know about HTTP
    // transport concerns like gzip.
    if (contentEncoding == "gzip")
    {
        using var input = new MemoryStream(body);
        using var gz = new GZipStream(input, CompressionMode.Decompress);
        using var output = new MemoryStream();
        gz.CopyTo(output);
        var decompressed = output.ToArray();
        var decompressedName = name.Replace(".envelope", ".decompressed.envelope");
        foreach (var dir in outDirs)
        {
            var outPath = Path.Combine(dir, decompressedName);
            File.WriteAllBytes(outPath, decompressed);
            Console.WriteLine($"wrote {outPath} ({decompressed.Length} bytes, decompressed)");
        }
    }
}

async Task Capture(string name, Action emit)
{
    // HttpListener can't bind port 0 directly on all platforms; find a free
    // port via a throwaway TcpListener first, then bind HttpListener to it.
    var probe = new System.Net.Sockets.TcpListener(IPAddress.Loopback, 0);
    probe.Start();
    var port = ((IPEndPoint)probe.LocalEndpoint).Port;
    probe.Stop();

    var listener = new HttpListener();
    listener.Prefixes.Add($"http://127.0.0.1:{port}/");
    listener.Start();

    var getContextTask = listener.GetContextAsync();

    using (SentrySdk.Init(o =>
    {
        // A numeric project ID: @sentry/node and sentry-sdk (Python) both
        // require this client-side; matching that constraint here too rather
        // than assuming .NET is more lenient without checking.
        o.Dsn = $"http://public@127.0.0.1:{port}/3";
        o.TracesSampleRate = 0;
        o.Debug = false;
        // Keep the captured envelope to exactly the one exception/message
        // item we're testing — session and client-report envelopes would
        // otherwise interleave with it since both default to enabled.
        o.AutoSessionTracking = false;
        o.SendClientReports = false;
    }))
    {
        emit();
        SentrySdk.Flush(TimeSpan.FromSeconds(2));
    }

    var completed = await Task.WhenAny(getContextTask, Task.Delay(TimeSpan.FromSeconds(5)));
    if (completed != getContextTask)
    {
        listener.Stop();
        throw new Exception($"no envelope captured for {name} (timed out)");
    }

    var ctx = await getContextTask;
    var contentEncoding = ctx.Request.Headers["Content-Encoding"];
    byte[] body;
    using (var ms = new MemoryStream())
    {
        await ctx.Request.InputStream.CopyToAsync(ms);
        body = ms.ToArray();
    }
    ctx.Response.StatusCode = 200;
    ctx.Response.Close();
    listener.Stop();

    if (body.Length == 0)
    {
        throw new Exception($"no envelope captured for {name} (empty body)");
    }
    WriteFixture(name, body, contentEncoding);
}

// Constructing an Exception without throwing it never populates its
// StackTrace in .NET — capturing that way would silently produce fixtures
// with zero frames, unlike what a real crash looks like. Throw/catch instead.
await Capture("sentry-dotnet-exception.envelope", () =>
{
    try
    {
        try
        {
            throw new Exception("inner cause");
        }
        catch (Exception inner)
        {
            throw new Exception("outer failure", inner);
        }
    }
    catch (Exception outer)
    {
        SentrySdk.CaptureException(outer);
    }
});

await Capture("sentry-dotnet-message.envelope", () =>
{
    SentrySdk.CaptureMessage("hello from trackdown fixture generator");
});

await Capture("sentry-dotnet-unhandled.envelope", () =>
{
    try
    {
        throw new InvalidOperationException("simulated unhandled-style error");
    }
    catch (InvalidOperationException e)
    {
        SentrySdk.CaptureException(e);
    }
});

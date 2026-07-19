// Captures real envelope bytes emitted by the Sentry Java SDK and writes
// them to every Go package's testdata/envelopes/ that needs them (mirrors
// tools/genfixtures-node's behavior for @sentry/node). Build/run with:
//   ./fetch-deps.sh   (downloads lib/sentry-8.49.0.jar, the SDK's only dependency)
//   javac -cp lib/sentry-8.49.0.jar Capture.java
//   java -cp .:lib/sentry-8.49.0.jar Capture        (or ".;lib\..." on Windows)
// Not part of the shipped product — dev tooling only, rerun after any Sentry
// Java SDK upgrade to catch wire format drift.
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import io.sentry.Sentry;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import java.util.zip.GZIPInputStream;

public class Capture {
    static final Path[] OUT_DIRS = {
        Paths.get("..", "..", "internal", "protocol", "testdata", "envelopes"),
        Paths.get("..", "..", "internal", "store", "testdata", "envelopes"),
        Paths.get("..", "..", "internal", "ingest", "testdata", "envelopes"),
        Paths.get("..", "..", "internal", "grouping", "testdata", "envelopes"),
    };

    public static void main(String[] args) throws Exception {
        for (Path dir : OUT_DIRS) {
            Files.createDirectories(dir);
        }

        capture("sentry-java-exception.envelope", () -> {
            try {
                try {
                    throw new RuntimeException("inner cause");
                } catch (RuntimeException inner) {
                    throw new RuntimeException("outer failure", inner);
                }
            } catch (RuntimeException outer) {
                Sentry.captureException(outer);
            }
        });

        capture("sentry-java-message.envelope", () ->
            Sentry.captureMessage("hello from trackdown fixture generator"));

        capture("sentry-java-unhandled.envelope", () -> {
            try {
                throw new IllegalStateException("simulated unhandled-style error");
            } catch (IllegalStateException e) {
                Sentry.captureException(e);
            }
        });
    }

    interface Emit {
        void run();
    }

    static void capture(String name, Emit emit) throws Exception {
        AtomicReference<byte[]> body = new AtomicReference<>();
        AtomicReference<String> contentEncoding = new AtomicReference<>();
        CountDownLatch latch = new CountDownLatch(1);

        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", (HttpExchange ex) -> {
            if (body.get() == null) {
                body.set(readAll(ex.getRequestBody()));
                List<String> enc = ex.getRequestHeaders().get("Content-Encoding");
                contentEncoding.set(enc == null || enc.isEmpty() ? null : enc.get(0));
                latch.countDown();
            }
            ex.sendResponseHeaders(200, -1);
            ex.close();
        });
        server.start();
        int port = server.getAddress().getPort();

        try {
            // A numeric project ID: @sentry/node and sentry-sdk (Python) both
            // require this client-side; matching that constraint here too
            // rather than assuming Java is more lenient without checking.
            Sentry.init(options -> {
                options.setDsn("http://public@127.0.0.1:" + port + "/3");
                options.setTracesSampleRate(0.0);
                options.setDebug(false);
                options.setEnableAutoSessionTracking(false);
                options.setSendClientReports(false);
            });

            emit.run();
            Sentry.flush(2000);
        } finally {
            Sentry.close();
        }

        if (!latch.await(5, TimeUnit.SECONDS)) {
            server.stop(0);
            throw new RuntimeException("no envelope captured for " + name + " (timed out)");
        }
        server.stop(0);

        byte[] raw = body.get();
        if (raw == null || raw.length == 0) {
            throw new RuntimeException("no envelope captured for " + name + " (empty body)");
        }
        writeFixture(name, raw, contentEncoding.get());
    }

    static byte[] readAll(InputStream in) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        int n;
        while ((n = in.read(buf)) != -1) {
            out.write(buf, 0, n);
        }
        return out.toByteArray();
    }

    static void writeFixture(String name, byte[] body, String contentEncoding) throws IOException {
        for (Path dir : OUT_DIRS) {
            Path outPath = dir.resolve(name);
            Files.write(outPath, body);
            System.out.println("wrote " + outPath + " (" + body.length + " bytes, content-encoding=" + contentEncoding + ")");
        }

        // Also write a decompressed sibling (mirrors genfixtures-python's
        // convention) so internal/protocol's parser-level tests can exercise
        // real envelope bytes directly without needing to know about HTTP
        // transport concerns like gzip.
        if ("gzip".equals(contentEncoding)) {
            byte[] decompressed;
            try (GZIPInputStream gz = new GZIPInputStream(new java.io.ByteArrayInputStream(body))) {
                decompressed = readAll(gz);
            }
            String decompressedName = name.replace(".envelope", ".decompressed.envelope");
            for (Path dir : OUT_DIRS) {
                Path outPath = dir.resolve(decompressedName);
                Files.write(outPath, decompressed);
                System.out.println("wrote " + outPath + " (" + decompressed.length + " bytes, decompressed)");
            }
        }
    }
}

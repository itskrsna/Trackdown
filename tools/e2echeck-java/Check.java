// Sends a real Sentry Java SDK event to an already-running Trackdown server,
// for manual end-to-end verification against the actual compiled binary
// (not just an in-process test server). Mirrors tools/e2echeck (Go).
// Build/run with:
//   ../genfixtures-java/fetch-deps.sh    (or copy lib/sentry-*.jar here)
//   javac -cp ../genfixtures-java/lib/sentry-8.49.0.jar Check.java
//   java -cp ".;../genfixtures-java/lib/sentry-8.49.0.jar" Check <host:port>
import io.sentry.Sentry;
import io.sentry.protocol.SentryId;

public class Check {
    public static void main(String[] args) {
        if (args.length < 1) {
            System.err.println("usage: java Check <host:port>");
            System.exit(2);
        }
        String host = args[0];

        // A numeric project ID: @sentry/node and sentry-sdk (Python) both
        // require this client-side (confirmed against real
        // "Invalid projectId"-style errors); matching that constraint here
        // too rather than assuming Java is more lenient without checking.
        String projectId = "828282";

        Sentry.init(options -> {
            options.setDsn("http://public@" + host + "/" + projectId);
            options.setTracesSampleRate(0.0);
            options.setDebug(false);
            options.setEnableAutoSessionTracking(false);
            options.setSendClientReports(false);
        });

        SentryId eventId;
        try {
            throw new RuntimeException("manual e2e check from Sentry Java SDK");
        } catch (RuntimeException e) {
            eventId = Sentry.captureException(e);
        }
        Sentry.flush(2000);
        Sentry.close();
        System.out.println("sent event_id=" + eventId + " to project " + projectId + " on " + host);
    }
}

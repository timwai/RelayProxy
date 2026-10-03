package hev.htproxy;

/** JNI entry points provided by the pinned hev-socks5-tunnel native library. */
public final class TProxyService {
    public interface FlowOwnerResolver {
        String resolve(
            int protocol,
            String sourceAddress,
            int sourcePort,
            String destinationAddress,
            int destinationPort
        );
    }

    private static volatile FlowOwnerResolver flowOwnerResolver;

    private TProxyService() {
    }

    static {
        System.loadLibrary("hev-socks5-tunnel");
    }

    public static native boolean TProxyStartService(String configPath, int fd);

    public static native boolean TProxyStopService();

    public static native boolean TProxyIsRunning();

    public static native long[] TProxyGetStats();

    public static void setFlowOwnerResolver(FlowOwnerResolver resolver) {
        flowOwnerResolver = resolver;
    }

    public static String TProxyResolveOwner(
        int protocol,
        String sourceAddress,
        int sourcePort,
        String destinationAddress,
        int destinationPort
    ) {
        FlowOwnerResolver resolver = flowOwnerResolver;
        if (resolver == null) return "__android_unknown__";
        String value = resolver.resolve(
            protocol,
            sourceAddress,
            sourcePort,
            destinationAddress,
            destinationPort
        );
        return value == null || value.isEmpty() ? "__android_unknown__" : value;
    }
}

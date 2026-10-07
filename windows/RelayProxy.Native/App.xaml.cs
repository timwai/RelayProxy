using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using RelayProxy.Native.Services;

namespace RelayProxy.Native;

public partial class App : Application
{
    private const string InstanceMutexName = @"Local\RelayProxyNativeUI_Instance";
    private const string ActivationEventName = @"Local\RelayProxyNativeUI_Show";

    private Window? _window;
    private Mutex? _instanceMutex;
    private EventWaitHandle? _activationEvent;

    public static AgentApiClient AgentApi { get; } = new();
    public static AgentProcessHost AgentHost { get; } = new(AgentApi);

    public App() => InitializeComponent();

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        _instanceMutex = new Mutex(initiallyOwned: true, InstanceMutexName, out var firstInstance);
        _activationEvent = new EventWaitHandle(false, EventResetMode.AutoReset, ActivationEventName);

        if (!firstInstance)
        {
            _activationEvent.Set();
            _instanceMutex.Dispose();
            _instanceMutex = null;
            Environment.Exit(0);
            return;
        }

        var dispatcher = DispatcherQueue.GetForCurrentThread();
        _window = new MainWindow();
        _window.Activate();

        _ = Task.Run(() =>
        {
            while (true)
            {
                try
                {
                    _activationEvent.WaitOne();
                }
                catch (ObjectDisposedException)
                {
                    return;
                }

                dispatcher.TryEnqueue(() =>
                {
                    if (_window is MainWindow main)
                        main.ShowFromExternalActivation();
                });
            }
        });

        _ = AgentHost.StartAsync();
    }
}

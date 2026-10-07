using Microsoft.UI.Xaml;
using RelayProxy.Native.Services;

namespace RelayProxy.Native;

public partial class App : Application
{
    private Window? _window;
    public static AgentApiClient AgentApi { get; } = new();
    public static AgentProcessHost AgentHost { get; } = new(AgentApi);

    public App() => InitializeComponent();

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        _window = new MainWindow();
        _window.Activate();
        _ = AgentHost.StartAsync();
    }
}

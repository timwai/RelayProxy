using System.Runtime.InteropServices;
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
    public static AppNotificationService Notifications { get; } = new();

    public App()
    {
        StartupDiagnostics.Write("App constructor starting.");
        UnhandledException += (_, e) => StartupDiagnostics.Write("Unhandled WinUI exception.", e.Exception);
        try
        {
            InitializeComponent();
            StartupDiagnostics.Write("XAML application initialized.");
        }
        catch (Exception ex)
        {
            StartupDiagnostics.Write("InitializeComponent failed.", ex);
            StartupDiagnostics.ShowFatal("RelayProxy 无法初始化 Windows UI。", ex);
            throw;
        }
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        StartupDiagnostics.Write($"OnLaunched: OS={Environment.OSVersion.VersionString}; Arch={RuntimeInformation.ProcessArchitecture}; Base={AppContext.BaseDirectory}");
        try
        {
            LaunchCore(args);
            StartupDiagnostics.Write("Main window launch completed.");
        }
        catch (Exception ex)
        {
            StartupDiagnostics.Write("OnLaunched failed.", ex);
            StartupDiagnostics.ShowFatal("RelayProxy 启动失败。已写入启动日志。", ex);
            Environment.Exit(1);
        }
    }

    private void LaunchCore(LaunchActivatedEventArgs args)
    {
        var launch = ParseLaunchOptions(Environment.GetCommandLineArgs().Skip(1));

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
        var main = new MainWindow();
        _window = main;
        Notifications.Initialize(dispatcher, main.ShowFromExternalActivation);
        if (!launch.Minimized)
            main.Activate();

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
                    if (_window is MainWindow existing)
                        existing.ShowFromExternalActivation();
                });
            }
        });

        _ = AgentHost.StartAsync(launch.ConfigPath);
    }

    private static NativeLaunchOptions ParseLaunchOptions(IEnumerable<string> args)
    {
        var options = new NativeLaunchOptions();
        var values = args.ToArray();
        for (var i = 0; i < values.Length; i++)
        {
            var value = values[i];
            if (value is "--minimized" or "--hidden")
            {
                options.Minimized = true;
                continue;
            }
            if (value.StartsWith("--config=", StringComparison.OrdinalIgnoreCase))
            {
                options.ConfigPath = value["--config=".Length..].Trim('"');
                continue;
            }
            if (string.Equals(value, "--config", StringComparison.OrdinalIgnoreCase) && i + 1 < values.Length)
            {
                options.ConfigPath = values[++i];
            }
        }
        return options;
    }

    private sealed class NativeLaunchOptions
    {
        public bool Minimized { get; set; }
        public string? ConfigPath { get; set; }
    }
}

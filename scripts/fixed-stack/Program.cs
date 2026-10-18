using System.Diagnostics;
using Microsoft.Diagnostics.Runtime;

int pid = int.Parse(args[0]);
using Process process = Process.GetProcessById(pid);
if (!string.Equals(Path.GetFileName(process.MainModule!.FileName), "powershell.exe", StringComparison.OrdinalIgnoreCase))
    throw new InvalidOperationException("Expected the test's Windows PowerShell child");
string dac = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Windows), "Microsoft.NET", "Framework64", "v4.0.30319", "mscordacwks.dll");
Stopwatch sampled = Stopwatch.StartNew();
try
{
    using DataTarget target = DataTarget.AttachToProcess(pid, suspend: true, new DataTargetOptions { SymbolPaths = Array.Empty<string>() });
    foreach (ClrInfo info in target.ClrVersions)
    {
        using ClrRuntime runtime = info.CreateRuntime(dac);
        foreach (ClrThread thread in runtime.Threads.Where(thread => thread.IsAlive))
        {
            foreach (ClrStackFrame frame in thread.EnumerateStackTrace().Take(30))
                Console.WriteLine($"managed pid={pid} thread={thread.OSThreadId} frame={frame}");
        }
    }
}
finally
{
    Console.WriteLine($"managed sampling completed pid={pid} suspended_scope_ms={sampled.ElapsedMilliseconds}");
}

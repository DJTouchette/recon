package index
import ("testing";"fmt";"github.com/djtouchette/recon/internal/scan")
func TestResolveNew(t *testing.T){
  var es []scan.FileEntry
  for _,p:=range []string{"src/foo/bar.lua","src/main.lua","app/util.zig","app/main.zig","sub/mod.jl","main.jl","lib.sh","run.sh"}{
    es=append(es, scan.FileEntry{RelPath:p, Lang:scan.LangFromExt(p), Class:scan.ClassSource})
  }
  idx:=NewFileIndex(es)
  fmt.Println("zig:", resolveZigSpecs([]string{"util.zig","std"}, "app/main.zig", idx))
  fmt.Println("lua:", resolveLuaSpecs([]string{"foo.bar"}, "src/main.lua", idx))
  fmt.Println("julia:", resolveJuliaSpecs([]string{"sub/mod.jl"}, "main.jl", idx))
  fmt.Println("shell:", resolveShellSpecs([]string{"./lib.sh","$X/y.sh"}, "run.sh", idx))
}

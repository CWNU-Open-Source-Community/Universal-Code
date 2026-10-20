package projectprep

import (
	"encoding/json"
	"strconv"
)

// PythonPreparationProgram keeps fixed product source in the reviewed argv.
// JSON chunks preserve the sandbox's single-line, 4 KiB argument limit.
func PythonPreparationProgram(testing bool) []string {
	source := PythonInstallScript
	if testing {
		source = PythonTestScript
	}
	var chunks []string
	for len(source) > 0 {
		end := min(len(source), 1024)
		encoded, _ := json.Marshal(source[:end])
		for len(encoded) > 3500 {
			end /= 2
			encoded, _ = json.Marshal(source[:end])
		}
		chunks = append(chunks, "json:"+string(encoded))
		source = source[end:]
	}
	bootstrap := "import json,sys;exec(''.join(json.loads(sys.argv.pop(1)[5:]) for _ in range(" + strconv.Itoa(len(chunks)) + ")))"
	return append([]string{"-I", "-B", "-c", bootstrap}, chunks...)
}

// PythonInstallScript is fixed product code. Repository content is decoded as
// bounded JSON data, never interpolated into Python source or a shell command.
// The actual interpreter supplies PEP 440/508 and compatible wheel semantics.
const pythonBoundPreparation = `import hashlib, json, os, pathlib, platform, subprocess, sys, tempfile
root = pathlib.Path(sys.argv[1])
if root.is_symlink() or not root.is_dir(): raise SystemExit('workspace root is linked or unavailable')
root = root.resolve()
def guarded(value):
 p = pathlib.Path(value)
 if not p.is_absolute(): p = root / p
 if not p.is_relative_to(root) or '..' in p.parts: raise SystemExit('Python preparation path escaped workspace')
 for parent in [p, *p.parents]:
  if parent == root: break
  if parent.is_symlink(): raise SystemExit('Python preparation path is linked')
 return p
def bounded(p):
 with p.open('rb') as stream: raw = stream.read(8388609)
 if len(raw) > 8388608: raise SystemExit('Python preparation input exceeds its byte limit')
 return raw
plan_path, expected = guarded(sys.argv[2]), sys.argv[3]
raw = bounded(plan_path)
if hashlib.sha256(raw).hexdigest() != expected: raise SystemExit('Python installation input changed')
plan = json.loads(raw)
for file in plan['files']:
 p = guarded(file['path'])
 if not p.is_file() or hashlib.sha256(bounded(p)).hexdigest() != file['sha256']: raise SystemExit('Python project or lock changed')
try:
 from pip._vendor.packaging.tags import sys_tags
 from pip._vendor.packaging.markers import default_environment
except ImportError as exc:
 raise SystemExit('Python preparation requires pip packaging support in the selected image: '+str(exc))
runtime = dict(python_build=sys.version, cache_tag=sys.implementation.cache_tag, environment=default_environment(), tags=[str(tag) for tag in sys_tags()])
runtime_key = hashlib.sha256((expected+json.dumps(runtime, sort_keys=True)).encode()).hexdigest()
`

const PythonInstallScript = pythonBoundPreparation + `try:
 import pip
 from pip._vendor.packaging.version import Version
 from pip._vendor.packaging.specifiers import SpecifierSet
 from pip._vendor.packaging.markers import Marker
 from pip._vendor.packaging.utils import parse_wheel_filename
except ImportError as exc:
 raise SystemExit('Python preparation requires pip >=25.3 in the selected image: '+str(exc))
if Version(pip.__version__) < Version('25.3'):
 raise SystemExit('Python preparation requires pip >=25.3 in the selected image')
environment = default_environment()
environment.update(extras=frozenset(), dependency_groups=frozenset(plan['default_groups'] or []))
def marker(value): return not value or Marker(value).evaluate(environment=environment, context='lock_file')
def requires(value): return not value or SpecifierSet(value).contains(platform.python_version(), prereleases=True)
if not all(requires(value) for value in plan['requires_python']): raise SystemExit('Selected Python does not satisfy requires-python')
if plan['environments'] and not any(marker(value) for value in plan['environments']): raise SystemExit('Selected Python environment is outside this lock')
wheel_dir = guarded(sys.argv[4])
site = guarded(pathlib.Path(sys.argv[5]) / runtime_key)
tags = {tag:index for index,tag in enumerate(sys_tags())}
selected, seen = [], set()
for package in plan['packages']:
 if not marker(package['marker']): continue
 name, version = package['name'], Version(package['version'])
 if name in seen: raise SystemExit('Ambiguous Python lock entries for '+name)
 seen.add(name)
 if not requires(package['requires_python']): raise SystemExit('Selected Python does not satisfy requires-python for '+name)
 candidates = []
 for wheel in package['wheels']:
  wheel_name, wheel_version, build, wheel_tags = parse_wheel_filename(wheel['name'])
  if wheel_name != name or wheel_version != version: raise SystemExit('Wheel identity does not match locked package '+name)
  ranks = [tags[tag] for tag in wheel_tags if tag in tags]
  if ranks: candidates.append((min(ranks), wheel['name'], wheel['hashes']['sha256'], wheel.get('size', -1)))
 if not candidates: raise SystemExit('No compatible locked wheel for '+name)
 _, filename, digest, size = min(candidates)
 wheel = guarded(wheel_dir / filename)
 if not wheel.is_file() or wheel.stat().st_size > 67108864: raise SystemExit('Staged Python wheel byte limit is invalid')
 if size >= 0 and wheel.stat().st_size != size: raise SystemExit('Staged Python wheel size mismatch')
 if hashlib.sha256(wheel.read_bytes()).hexdigest() != digest: raise SystemExit('Staged Python wheel integrity mismatch')
 selected.append(wheel.as_uri()+' --hash=sha256:'+digest)
site.parent.mkdir(parents=True, exist_ok=True)
if site.exists():
 if not site.is_dir(): raise SystemExit('Private Python environment is not a directory')
 print('Reusing matching task-private Python environment')
 raise SystemExit(0)
with tempfile.TemporaryDirectory(prefix='.python-install-', dir=site.parent) as temporary:
 temporary = pathlib.Path(temporary)
 target = temporary / 'site'
 target.mkdir()
 if selected:
  requirements = temporary / 'requirements.txt'
  requirements.write_text('\n'.join(selected)+'\n', encoding='utf-8')
  command = [sys.executable, '-I', '-B', '-m', 'pip', '--isolated', '--disable-pip-version-check', '--no-input', '--no-cache-dir', 'install', '--no-index', '--no-deps', '--require-hashes', '--only-binary=:all:', '--no-compile', '--target', str(target), '-r', str(requirements)]
  result = subprocess.run(command, cwd=temporary)
  if result.returncode: raise SystemExit(result.returncode)
 os.rename(target, site)
print('Prepared task-private Python environment; selected packages:', len(selected))
`

// PythonTestScript keeps the original unittest/pytest arguments. Its child
// receives only the matching task-private site through PYTHONPATH.
const PythonTestScript = pythonBoundPreparation + `site = guarded(pathlib.Path(sys.argv[4]) / runtime_key)
if not site.is_dir(): raise SystemExit('Prepare Python dependencies for the selected interpreter before running this test')
module = sys.argv[5]
if module not in ('unittest', 'pytest'): raise SystemExit('Unsupported Python project test module')
environment = dict(os.environ, PYTHONPATH=str(site), PYTHONNOUSERSITE='1')
raise SystemExit(subprocess.run([sys.executable, '-B', '-m', module, *sys.argv[6:]], env=environment).returncode)
`

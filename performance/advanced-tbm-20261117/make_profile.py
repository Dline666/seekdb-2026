from pathlib import Path
import shutil
import sys
here=Path(__file__).resolve().parent
root=Path('/home/platemo/seekdb-experiments/20261117/experiment-runtime')
source=Path(sys.argv[1]) if len(sys.argv)>1 else root/'remote-final'
dest=root/(sys.argv[2] if len(sys.argv)>2 else 'advanced-profile-source')
shutil.copytree(source,dest)
p=dest/'cmd/minivectordb/main.go';s=p.read_text()
s=s.replace('func main() {','func main() {\nadvancedProbeInit()')
s=s.replace('\tout := bufio.NewWriterSize','\tadvancedProbeReady()\n\tout := bufio.NewWriterSize')
s=s.replace('\t\tres, err := db.Exec(*req.SQL)','\t\tadvancedProbeBefore(*req.SQL)\n\t\tres, err := db.Exec(*req.SQL)')
s=s.replace('\t\tcase "shutdown":','\t\tcase "shutdown":\n\t\t\tadvancedProbeFinish()')
s=s.replace('os.Exit(','profileExit(')
p.write_text(s)
(dest/'cmd/minivectordb/advanced_profile.go').write_text((here/'profile_probe.go.txt').read_text())
print(dest)

from common import *
command('format-tbm-driver-v2', ['/tmp/seekdb-round3-go/bin/gofmt','-w',OUTPUT/'tbm_eval.go'])
command('build-tbm-driver-v2', [GO,'build','-trimpath','-o',ROOT/'bin/tbm-eval',OUTPUT/'tbm_eval.go'])

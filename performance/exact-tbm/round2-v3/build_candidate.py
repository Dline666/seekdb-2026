from common import *
command('format-poll-default', ['/tmp/seekdb-round3-go/bin/gofmt','-w',
    REPO/'minivectordb/cmd/minivectordb/stdin_poll.go', REPO/'minivectordb/cmd/minivectordb/stdin_poll_test.go'])
source=snapshot('candidate')
command('candidate-tests', [GO,'test','./...','-count=1','-timeout=8m'],cwd=source,timeout=600)
command('build-candidate', [GO,'build','-trimpath','-o',ROOT/'bin/candidate','./cmd/minivectordb'],cwd=source)
save('binaries.json', {name:sha(ROOT/'bin'/name) for name in ['base','v2','memory','candidate']})
command('candidate-race', [GO,'test','-race','./internal/engine','./internal/sqlparse',
    './cmd/minivectordb','-count=1','-timeout=8m'],cwd=source,extra={'CGO_ENABLED':'1'},timeout=600)

import ctypes,json,os,platform
lib=ctypes.CDLL(None,use_errno=True)
class Attr(ctypes.Structure):
 _fields_=[('type',ctypes.c_uint32),('size',ctypes.c_uint32),('config',ctypes.c_uint64),('period',ctypes.c_uint64),('sample_type',ctypes.c_uint64),('read_format',ctypes.c_uint64),('flags',ctypes.c_uint64)]+[('reserved'+str(i),ctypes.c_uint64) for i in range(8)]
a=Attr(type=0,size=ctypes.sizeof(Attr),config=0,flags=(1<<0)|(1<<5)|(1<<6))
assert platform.machine()=='x86_64'
fd=lib.syscall(298,ctypes.byref(a),0,-1,-1,0)
r={'uid':os.getuid(),'perf_event_paranoid':open('/proc/sys/kernel/perf_event_paranoid').read().strip(),'cpu_cycles_event_open':fd>=0,'errno':ctypes.get_errno() if fd<0 else 0}
if fd>=0:os.close(fd)
print(json.dumps(r))

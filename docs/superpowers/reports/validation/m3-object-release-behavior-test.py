import ast,copy,io,json,tempfile,contextlib,types,os,signal
from pathlib import Path
SRC=Path(__file__).with_name('m3-object-release-prepared.py').read_text(); tree=ast.parse(SRC)
main=next(i for i,n in enumerate(tree.body) if isinstance(n,ast.Try))
manifest=json.loads(Path(__file__).with_name('m3-object-diagnostic-candidate.json').read_text())
constants={n.targets[0].id:ast.literal_eval(n.value) for n in tree.body if isinstance(n,ast.Assign) and isinstance(n.targets[0],ast.Name) and n.targets[0].id in ['OLD','OLDIMAGE','IMAGE','REV','ARCHIVE_SHA','BINARY_SHA']}
assert constants['OLD']==manifest['publication']['currentContainerId']
assert constants['OLDIMAGE']==manifest['publication']['currentImageId']
assert constants['IMAGE']==manifest['imageId'] and constants['IMAGE']!=constants['OLDIMAGE']
assert constants['REV']==manifest['sourceRevision'] and constants['ARCHIVE_SHA']==manifest['archiveSHA256']
assert constants['BINARY_SHA']==manifest['binarySHA256']

signals={s:signal.getsignal(s) for s in [signal.SIGINT,signal.SIGTERM,signal.SIGHUP]}
cases=['success','create_failure','health_failure','cleanup_failure','identity_mismatch','lock_failure','health_interrupt']
for case in cases:
 with tempfile.TemporaryDirectory(prefix='m3-release-test-') as folder:
  ns={}; exec(compile(ast.Module(body=tree.body[:main],type_ignores=[]),'release-defs','exec'),ns)
  root=Path(folder);root.chmod(0o700);(root/'image.tar').write_bytes(b'fixture');ns['ROOT']=root
  old={'Id':ns['OLD'],'Image':ns['OLDIMAGE'],'Name':'/ai-gateway-go','State':{'Running':True},'Mounts':[], 'Config':{'Hostname':'fixture','Domainname':'','User':'','Env':['PRIVATE_SENTINEL=DO_NOT_PRINT','APP_ENV=production'],'Cmd':['./server'],'WorkingDir':'/app','Volumes':None,'Labels':copy.deepcopy(ns['EXPECTED_OLD_LABELS'])},'HostConfig':{'NetworkMode':'porsche-app','PortBindings':{'8000/tcp':[{'HostIp':'127.0.0.1','HostPort':'8000'}]},'RestartPolicy':{'Name':'no','MaximumRetryCount':0},'AutoRemove':False},'NetworkSettings':{'Networks':{'porsche-app':{}}}}
  states={ns['OLD']:old}; events=[];new='a'*64; original_require=ns['require']; lockcount=[0]
  def require(value,message):
   if message=='unsafe_staging':return
   original_require(value,message)
  ns['require']=require
  def inspect(cid):
   if cid=='ai-gateway-go':
    matches=[v for v in states.values() if v['Name']=='/ai-gateway-go'];require(len(matches)==1,'missing_name');return copy.deepcopy(matches[0])
   if cid not in states:raise ns['Gate']('missing_id')
   return copy.deepcopy(states[cid])
  def cmd(args,timeout=45):
   events.append(('cmd',args[:3]))
   if args[:3]==['docker','image','load']:return b'ok'
   if args[:3]==['docker','image','inspect']:return json.dumps([{'Architecture':'amd64','Os':'linux','Config':{'Labels':{'org.opencontainers.image.revision':ns['REV']}}}]).encode()
   if args[:2]==['docker','run']:return ''.join(v+'  /app/'+k+'\n' for k,v in ns['BINARY_SHA'].items()).encode()
   if args[:2]==['docker','inspect']:return json.dumps([inspect(args[2])]).encode()
   raise AssertionError(args)
  def api(method,path,data=None):
   events.append(('api',method,path))
   if path.startswith('/containers/create'):
    if case=='create_failure':raise ns['Gate']('mock_create_failure')
    c=copy.deepcopy(old);c['Id']=new;c['Image']=ns['IMAGE'];c['Name']='/candidate';c['State']['Running']=False
    c['Config']={k:v for k,v in data.items() if k not in ['HostConfig','NetworkingConfig']};c['HostConfig']=data['HostConfig'];states[new]=c
    return {'Id':new}
   cid=path.split('/')[2].split('?')[0]
   if method=='DELETE':
    states.pop(cid,None)
    if case=='cleanup_failure':raise ns['Gate']('mock_cleanup_response_failure')
   elif '/stop?' in path:states[cid]['State']['Running']=False
   elif '/rename?' in path:
    name='/'+path.split('name=')[1];require(not any(x['Name']==name and x['Id']!=cid for x in states.values()),'name_conflict');states[cid]['Name']=name
   elif path.endswith('/start'):states[cid]['State']['Running']=True
   else:raise AssertionError((method,path))
  def healthy(cid):
   events.append(('health',cid))
   if cid==new:
    if case=='identity_mismatch':states[new]['Image']='sha256:wrong';return False
    if case=='health_interrupt':raise ns['Interrupted']()
    return case not in ['health_failure','cleanup_failure']
   return True
  def flock(fd,flags):
   lockcount[0]+=1
   if case=='lock_failure' and lockcount[0]==2:raise BlockingIOError()
  class OSProxy:
   def __getattr__(self,k):return getattr(os,k)
   def open(self,path,*args,**kwargs):
    if str(path).startswith('/var/lock/'):path=str(root/Path(path).name)
    return os.open(path,*args,**kwargs)
  ns.update(cmd=cmd,api=api,inspect=inspect,healthy=healthy,frontend=lambda:None,guards=lambda:events.append(('guards',)),digest=lambda p:ns['ARCHIVE_SHA'],fcntl=types.SimpleNamespace(LOCK_EX=1,LOCK_NB=2,flock=flock),os=OSProxy())
  stream=io.StringIO();exitcode=0
  try:
   with contextlib.redirect_stdout(stream):exec(compile(ast.Module(body=tree.body[main:],type_ignores=[]),'release-main','exec'),ns)
  except SystemExit as e:exitcode=e.code
  output=stream.getvalue();assert 'DO_NOT_PRINT' not in output and 'PRIVATE_SENTINEL' not in output
  api_events=[x for x in events if x[0]=='api'];stops=[x for x in api_events if '/stop?' in x[2]]
  if case=='success':assert exitcode==0 and states[new]['State']['Running'] and not states[ns['OLD']]['State']['Running']
  elif case in ('create_failure','lock_failure'):
   assert exitcode==1 and not stops and states[ns['OLD']]['State']['Running']
   if case=='lock_failure':assert not any(x[0]=='guards' for x in events) and not (root/'runtime-private.json').exists()
  elif case=='identity_mismatch':
   assert exitcode==1 and not any(x[1]=='DELETE' for x in api_events) and 'rollback_failed' in output
  else:
   assert exitcode==1 and states[ns['OLD']]['Name']=='/ai-gateway-go' and states[ns['OLD']]['State']['Running'] and 'rollback_verified' in output
   if case=='cleanup_failure':assert 'rollback_failed' in output
  print('PASS',case)
for sig,handler in signals.items():signal.signal(sig,handler)
print('7 behavioral cases PASS; mocked Docker/network only; production untouched.')

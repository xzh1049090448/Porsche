#!/usr/bin/env python3
"""Exact M3 deployment. Secrets stay in the root-only server staging directory."""
import os,sys,json,socket,http.client,subprocess,hashlib,time,re,fcntl,stat,copy,signal
from pathlib import Path
ROOT=Path('/var/tmp/porsche-m3-object-release-ad3f5b4-20260903')
OLD='13ada4aa4f1e4460265d778f3447957ea854b234b542e509ffcf6668ef7b3031'
OLDIMAGE='sha256:cb42daed3581c51a5e6f5ac5a237bec4b4e0c3540afb61d36acb27592dc2947f'
IMAGE='sha256:2bc6b866911f439545bd3128bf8af24e640b3922706c024651d3a7d2d15096d0'
ARCHIVE_SHA='8154e46b93466f13018b8d310a4caf361bc40989cfd5e603e74ffe9ca94eb280'
REV='ad3f5b4416854353ebb2b3647ae4b2a809b4a05c'
EXPECTED_OLD_LABELS={'org.opencontainers.image.revision': '6e70784e182a4bae1e5b8399053b45429e8e4400', 'codex.task': 'm3-chunk-diagnostic-candidate'}
JS_SHA='7c133cb1197c701cc6288d2718384c92f8b4b24a15fd13d821f3930d8bf09033'
BINARY_SHA={'server': 'c0fb79a40874a2d3067d2733e6b9e17b0d5e3884c50db7ebbf0de296e7af71c1', 'bootstrap-root': '3fdf6868151c180cb412c10580c466fe51a14a027941ff4d4b2bdc30107ed15e'}
class Gate(Exception): pass
class Interrupted(Exception): pass
class UnixHTTP(http.client.HTTPConnection):
 def connect(self):
  self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); self.sock.settimeout(self.timeout); self.sock.connect('/var/run/docker.sock')
def api(method,path,data=None):
 c=UnixHTTP('localhost',timeout=40)
 try:
  body=None if data is None else json.dumps(data).encode()
  c.request(method,'/v1.47'+path,body,{'Content-Type':'application/json'})
  r=c.getresponse(); raw=r.read()
  if r.status>=300: raise Gate('docker_api_status_'+str(r.status))
  return json.loads(raw) if raw else None
 finally: c.close()
def cmd(args,timeout=45):
 r=subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout)
 if r.returncode: raise Gate('command_failed')
 return r.stdout
def inspect(cid): return json.loads(cmd(['docker','inspect',cid]))[0]
def require(x,message):
 if not x: raise Gate(message)
def digest(path):
 h=hashlib.sha256()
 with open(path,'rb') as f:
  for chunk in iter(lambda:f.read(1048576),b''): h.update(chunk)
 return h.hexdigest()
def emit(event,**kwargs): print(json.dumps({'event':event,**kwargs}),flush=True)
def save(name,value):
 target=ROOT/name;temporary=ROOT/(name+'.pending')
 require(not target.exists() and not target.is_symlink(),'snapshot_already_exists')
 fd=os.open(str(temporary),os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
 with os.fdopen(fd,'w') as f:
  json.dump(value,f,indent=2);f.write('\n');f.flush();os.fsync(f.fileno())
 os.link(str(temporary),str(target),follow_symlinks=False);temporary.unlink()
 directory=os.open(str(ROOT),os.O_DIRECTORY);os.fsync(directory);os.close(directory)
def guards():
 ids=cmd(['docker','ps','-aq']).decode().split()
 for cid in ids:
  c=inspect(cid); name=c['Name'].lstrip('/')
  if name=='ai-gateway-go' or re.fullmatch(r'ai-gateway-go-acceptance-rollback-\d+',name):
   require(not any(e.split('=',1)[0].startswith('ROOT_BOOTSTRAP_') for e in c['Config'].get('Env',[])),'root_runtime_key')
def curl(url,extra=()):
 raw=cmd(['curl','--fail','--silent','--show-error','--connect-timeout','2','--max-time','3','--write-out','\n%{http_code}',*extra,url],timeout=5)
 body,code=raw.rsplit(b'\n',1);require(code==b'200','http_status_not_200');return body
def healthy(cid):
 deadline=time.monotonic()+90
 while time.monotonic()<deadline:
  try:
   require(inspect(cid)['State']['Running'],'not_running')
   curl('http://127.0.0.1:8000/health',['-H','Host: aiportcloud.com'])
   curl('https://aiportcloud.com/health')
   return True
  except (Gate,subprocess.TimeoutExpired): time.sleep(2)
 return False
def frontend():
 require(b'index-i7ZWPv9J.js' in curl('https://aiportcloud.com/'),'frontend_entry_changed')
 require(hashlib.sha256(curl('https://aiportcloud.com/assets/index-i7ZWPv9J.js')).hexdigest()==JS_SHA,'frontend_hash_changed')
def same_runtime(new,old):
 for key,val in old['HostConfig'].items():
  observed=new['HostConfig'].get(key)
  # Docker create normalizes unset OOM-disable to false; neither disables OOM killing.
  if key=='OomKillDisable' and val is None and observed is False: continue
  require(observed==val,'host_config_mismatch_'+key)
 for key,val in old['Config'].items():
  if key not in ('Image','Labels'): require(new['Config'].get(key)==val,'config_mismatch_'+key)
 require(new['Image']==IMAGE,'candidate_image_mismatch')
 require(new['Mounts']==old['Mounts'],'mount_mismatch')
 require(set(new['NetworkSettings']['Networks'])=={'porsche-app'},'network_mismatch')
 require(new['Config']['Labels'].get('org.opencontainers.image.revision')==REV,'revision_mismatch')
def interrupted(signum,frame): raise Interrupted('interrupted')
signal.signal(signal.SIGINT,interrupted)
signal.signal(signal.SIGTERM,interrupted)
signal.signal(signal.SIGHUP,interrupted)
locks=[];newid=None;touched=False;stage='preflight';rollback_name='ai-gateway-go-acceptance-rollback-'+str(time.time_ns());success=False
try:
 os.umask(0o077)
 st=ROOT.lstat();require(stat.S_ISDIR(st.st_mode) and st.st_uid==0 and stat.S_IMODE(st.st_mode)==0o700,'unsafe_staging')
 require(not (ROOT/'image.tar').is_symlink(),'unsafe_archive')
 require(digest(ROOT/'image.tar')==ARCHIVE_SHA,'archive_hash')
 cmd(['docker','image','load','-i',str(ROOT/'image.tar')],timeout=120)
 image=json.loads(cmd(['docker','image','inspect',IMAGE]))[0]
 require(image['Architecture']=='amd64' and image['Os']=='linux','image_platform')
 require(image['Config']['Labels'].get('org.opencontainers.image.revision')==REV,'image_revision')
 hashes=cmd(['docker','run','--rm','--network','none','--entrypoint','/bin/sh',IMAGE,'-c','test -s /etc/ssl/certs/ca-certificates.crt && sha256sum /app/server /app/bootstrap-root']).decode().splitlines()
 require({line.split()[1].split('/')[-1]:line.split()[0] for line in hashes}==BINARY_SHA,'binary_hash')
 emit('image_verified',imageId=IMAGE,archiveSHA256=ARCHIVE_SHA)
 for name in ['porsche-full-stack.deploy.lock','porsche-auth-acceptance.deploy.lock','ai-gateway-go.deploy.lock']:
  fd=os.open('/var/lock/'+name,os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600);locks.append(fd);fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
 stage='locked_preflight';guards();old=inspect('ai-gateway-go')
 require(old['Id']==OLD and old['Image']==OLDIMAGE and old['State']['Running'],'old_runtime_drift')
 require(old['Mounts']==[] and old['Config'].get('Volumes') in (None,{}),'mount_drift')
 require(old['Config']['Cmd']==['./server'] and old['Config']['WorkingDir']=='/app' and old['Config']['User']=='','entry_drift')
 require(old['HostConfig']['NetworkMode']=='porsche-app' and set(old['NetworkSettings']['Networks'])=={'porsche-app'},'network_drift')
 require(old['HostConfig']['PortBindings']=={'8000/tcp':[{'HostIp':'127.0.0.1','HostPort':'8000'}]},'port_drift')
 require(old['HostConfig']['RestartPolicy']=={'Name':'no','MaximumRetryCount':0} and not old['HostConfig']['AutoRemove'],'restart_drift')
 net=old['NetworkSettings']['Networks']['porsche-app']
 require(all(net.get(k) in (None,[],{},0) for k in ['IPAMConfig','Links','Aliases','DriverOpts','GwPriority']),'custom_network_config')
 require(old['Config'].get('Labels')==EXPECTED_OLD_LABELS,'label_drift')
 require(not any(e.split('=',1)[0].startswith('ROOT_BOOTSTRAP_') for e in old['Config']['Env']),'root_runtime_key')
 require(healthy(OLD),'baseline_health');frontend()
 save('runtime-private.json',{'Config':old['Config'],'HostConfig':old['HostConfig']})
 payload=copy.deepcopy(old['Config']);payload['Image']=IMAGE
 payload['Labels']={'org.opencontainers.image.revision':REV,'codex.task':'m3-object-diagnostic-candidate'}
 payload['HostConfig']=copy.deepcopy(old['HostConfig'])
 payload['NetworkingConfig']={'EndpointsConfig':{'porsche-app':{}}}
 # JSON preserves exact environment values without env-file conversion or secret argv.
 stage='create_candidate'
 newid=api('POST','/containers/create?name=ai-gateway-go-m3-candidate-'+str(time.time_ns()),payload)['Id']
 require(re.fullmatch('[a-f0-9]{64}',newid),'invalid_new_id');same_runtime(inspect(newid),old)
 save('deployment-identities.json',{'oldContainerId':OLD,'oldImageId':OLDIMAGE,'newContainerId':newid,'newImageId':IMAGE,'rollbackName':rollback_name,'archiveSHA256':ARCHIVE_SHA})
 emit('candidate_preflight_pass',newContainerId=newid,oldContainerId=OLD)
 if '--preflight-only' in sys.argv:
  api('DELETE','/containers/'+newid);newid=None;emit('preflight_only_complete');sys.exit(0)
 stage='switch';guards();require(inspect('ai-gateway-go')['Id']==OLD,'old_drift_before_stop');touched=True
 api('POST','/containers/'+OLD+'/stop?t=15')
 api('POST','/containers/'+OLD+'/rename?name='+rollback_name)
 api('POST','/containers/'+newid+'/rename?name=ai-gateway-go')
 api('POST','/containers/'+newid+'/start')
 stage='candidate_health';require(healthy(newid),'candidate_health_failed');same_runtime(inspect(newid),old);frontend()
 require(inspect('ai-gateway-go')['Id']==newid,'active_id_mismatch')
 require(not inspect(OLD)['State']['Running'],'old_still_running')
 result={'status':'deployed','sourceRevision':REV,'imageId':IMAGE,'archiveSHA256':ARCHIVE_SHA,'newContainerId':newid,'oldContainerId':OLD,'rollbackName':rollback_name,'sourceHealth':200,'publicHealth':200,'frontendUnchanged':True,'generationRequests':0,'utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())}
 save('release-result.json',result);success=True;emit('release_success',**result)
except Exception as e:
 emit('release_error',stage=stage,reason=str(e) if isinstance(e,Gate) else type(e).__name__)
 for sig in (signal.SIGINT,signal.SIGTERM,signal.SIGHUP): signal.signal(sig,signal.SIG_IGN)
 errors=[]
 if newid:
  try:
   n=inspect(newid);require(n['Image']==IMAGE,'rollback_candidate_identity');api('DELETE','/containers/'+newid+'?force=true')
  except Exception as r: errors.append('candidate_cleanup:'+ (str(r) if isinstance(r,Gate) else type(r).__name__))
 if touched:
  try:
   c=inspect(OLD);require(c['Image']==OLDIMAGE,'rollback_old_identity')
   if c['Name']!='/ai-gateway-go': api('POST','/containers/'+OLD+'/rename?name=ai-gateway-go')
   if not inspect(OLD)['State']['Running']: api('POST','/containers/'+OLD+'/start')
   require(healthy(OLD),'rollback_health_failed');emit('rollback_verified',oldContainerId=OLD)
  except Exception as r: errors.append('old_restore:'+ (str(r) if isinstance(r,Gate) else type(r).__name__))
 else: emit('old_service_not_stopped')
 if errors: emit('rollback_failed',reasons=errors)
 sys.exit(1)
finally:
 if success or not touched:
  p=ROOT/'runtime-private.json'
  if p.exists() and not p.is_symlink():p.unlink()
 for fd in reversed(locks): os.close(fd)

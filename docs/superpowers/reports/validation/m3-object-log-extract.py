import subprocess,json,re,sys
CID,SHA,REV,IMAGE=sys.argv[1:]
assert re.fullmatch('[0-9a-f]{64}',CID) and re.fullmatch('[0-9a-f]{64}',SHA)
assert re.fullmatch('[0-9a-f]{40}',REV) and re.fullmatch('sha256:[0-9a-f]{64}',IMAGE)
def command(args):
 r=subprocess.run(args,capture_output=True);assert r.returncode==0,'read_command_failed';return r.stdout
c=json.loads(command(['docker','inspect',CID]))[0]
assert c['Image']==IMAGE
r=subprocess.run(['docker','logs','--since',c['State']['StartedAt'],CID],capture_output=True);assert r.returncode==0
keys='event trace_id request_id_sha256 utc route method vcs_revision vcs_modified duration_ms http_status upstream_http_status upstream_request_attempted upstream_response_received first_frame_emitted daily_call_saved conversation_ready user_message_saved final_saved'.split()
stages='authentication validation model_catalog_acl quota_persist conversation_lookup_create user_message_save title_save serialization upstream_connect sse_stream assistant_save usage_save final_write'.split()
reasons='none rejected database_error invalid_request network_error dns_error tls_error connection_error timeout canceled upstream_non_2xx redirect_rejected malformed_chunk early_eof stream_read_error client_write_error unavailable'.split()
fields='unknown chunk id object created usage usage.prompt_tokens usage.completion_tokens usage.total_tokens choices choices[].index choices[].finish_reason choices[].delta choices[].delta.role choices[].delta.content choices[].delta.refusal choices[].delta.tool_calls choices[].delta.tool_calls[].index choices[].delta.tool_calls[].id choices[].delta.tool_calls[].type choices[].delta.tool_calls[].function choices[].delta.tool_calls[].function.name choices[].delta.tool_calls[].function.arguments'.split()
records=[]
for line in (r.stdout+b'\n'+r.stderr).splitlines():
 try:d=json.loads(line)
 except (ValueError,UnicodeError):continue
 if not isinstance(d,dict) or d.get('event')!='platform_chat_diagnostic' or d.get('request_id_sha256')!=SHA:continue
 out={k:d[k] for k in keys if k in d}
 assert out['route']=='/api/v1/platform/chat/completions' and out['method']=='POST'
 assert re.fullmatch('[0-9a-f]{32}',out['trace_id']) and re.fullmatch('[0-9a-f]{40}',out['vcs_revision'])
 out['stages']={}
 for s in stages:
  v=d.get('stages',{}).get(s,{})
  out['stages'][s]={'state':v.get('state') if v.get('state') in ['success','failed','not_run'] else 'unknown','reason':v.get('reason') if v.get('reason') in reasons else 'unavailable','duration_ms':v.get('duration_ms') if isinstance(v.get('duration_ms'),(float,int)) else None}
 v=d.get('malformed_chunk_detail',{})
 out['malformed_chunk_detail']={'reason':v.get('reason') if v.get('reason') in ['unknown','json_syntax','json_type','missing_required','invalid_value','negative_value','invalid_shape'] else 'unknown','field':v.get('field') if v.get('field') in fields else 'unknown'}
 if 'object_detail' in v:
  q=v['object_detail'];assert isinstance(q,dict)
  out['malformed_chunk_detail']['object_detail']={
   'decoded_kind':q.get('decoded_kind') if q.get('decoded_kind') in ['empty','known_chat_completion','other','unknown'] else 'unknown',
   'field_shape':q.get('field_shape') if q.get('field_shape') in ['missing','null','string_empty','string_nonempty','ambiguous','unknown'] else 'unknown',
   'key_match':q.get('key_match') if q.get('key_match') in ['canonical','case_variant','multiple','none','unknown'] else 'unknown'
  }
 assert out['vcs_revision']==REV and out['vcs_modified']=='false'
 records.append(out)
assert len(records)==1,'expected_one_correlated_record'
print(json.dumps({'containerId':CID,'startedAt':c['State']['StartedAt'],'running':c['State']['Running'],'matchedRecords':records},indent=2))

package lark

const chatSearchFixture = `if a[:2] in [['im','+chat-search'],['contact','+search-user'],['contact','+search-bot']]:
 assert a[-4:]==['--as','user','--format','json']
 assert a[a.index('--query')+1]=='研发'
 assert '--page-all' not in a and '--exclude-muted' not in a and '--exclude-external-users' not in a
 open(base+'/read','w').write('yes')
 kind='group' if a[0]=='im' else 'user' if a[1]=='+search-user' else 'bot'
 if mode==kind+'_permission':
  print(json.dumps({'ok':False,'error':{'type':'authorization'}}),file=sys.stderr);sys.exit(1)
 if kind=='group':
  assert a[a.index('--search-types')+1]=='private,public_joined,external'
  assert a[a.index('--chat-modes')+1]=='group,topic' and '--disable-search-by-user' in a
  data={'chats':[{'chat_id':'oc_group','chat_mode':'DEFAULT','name':'研发群'},{'chat_id':'oc_topic','chat_mode':'THREAD','name':'研发话题'}],'has_more':True,'page_token':'next'}
  if '--page-token' in a:
   assert a[a.index('--page-token')+1]=='next'
   data={'chats':[],'has_more':False,'page_token':''}
  if mode=='bad_mode':data['chats'][0]['chat_mode']='p2p'
  if mode=='bad_cursor':data['page_token']=''
 else:
  assert '--has-chatted' in a and '--page-token' not in a
  if kind=='user':
   data={'users':[{'open_id':'ou_peer','localized_name':'同事','p2p_chat_id':'oc_peer'},{'open_id':'ou_empty','localized_name':'未聊过','p2p_chat_id':''}],'has_more':True}
   if mode=='bad_chat':data['users'][0]['p2p_chat_id']='ou_not_chat'
   if mode=='bad_name':data['users'][0]['localized_name']='bad\nname'
  else:
   data={'bots':[{'open_id':'ou_bot','name':'助手','chat_id':'oc_bot'}],'has_more':True}
   if mode=='duplicate':data['bots'][0]['chat_id']='oc_peer'
 if mode=='missing_more':del data['has_more']
 print(json.dumps({'ok':True,'identity':'bot' if mode=='response_bot' else 'user','data':data}));sys.exit()
`

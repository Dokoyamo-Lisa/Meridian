import json, sys
# make-client.py SINGBOX_JSON TAG PORT - a sing-box client config that sends everything through one
# outbound (TAG) and offers a SOCKS/HTTP proxy on 127.0.0.1:PORT.
cfg=json.load(open(sys.argv[1])); tag=sys.argv[2]; port=int(sys.argv[3])
cfg['inbounds']=[{"type":"mixed","tag":"mixed-in","listen":"127.0.0.1","listen_port":port}]
cfg['route']={"rules":[{"action":"sniff"}],"final":tag,"default_domain_resolver":"dns-direct"}
cfg['dns']={"servers":[{"type":"udp","tag":"dns-direct","server":"8.8.8.8"}],"final":"dns-direct"}
cfg['outbounds']=[o for o in cfg['outbounds'] if o['type'] not in ('selector','urltest')]
cfg.pop('experimental',None)
print(json.dumps(cfg))

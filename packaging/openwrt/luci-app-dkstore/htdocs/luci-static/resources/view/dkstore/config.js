'use strict';
'require view';
'require form';
'require rpc';

var callStatus = rpc.declare({
	object: 'luci.dkstore',
	method: 'status',
	expect: { '': {} }
});

function isZh() {
	var lang = (document.documentElement.getAttribute('lang') || navigator.language || '').toLowerCase();
	return lang.indexOf('zh') === 0;
}

return view.extend({
	load: function() {
		return callStatus().catch(function() {
			return { running: false, port: 8090 };
		});
	},

	render: function(st) {
		var zh = isZh();
		var running = !!(st && st.running);
		var port = (st && st.port) ? st.port : 8090;

		var desc = zh
			? 'DKStore 跨平台 Docker 应用更新中心（当前状态：' + (running ? '运行中，监听端口 ' + port : '已停止') + '）。'
			: 'DKStore Cross-Platform Docker App Update Center (Status: ' + (running ? 'Running on port ' + port : 'Stopped') + ').';

		var m = new form.Map('dkstore', 'DKStore', desc);
		var s = m.section(form.NamedSection, 'main', 'dkstore', zh ? '基本设置' : 'Basic Settings');
		s.anonymous = true;
		s.addremove = false;

		var o;
		o = s.option(form.Flag, 'enabled', zh ? '启用服务' : 'Enable Service');
		o.rmempty = false;
		o.default = '1';

		o = s.option(form.Value, 'port', zh ? 'Web 监听端口' : 'Listen Port');
		o.datatype = 'port';
		o.placeholder = '8090';
		o.default = '8090';
		o.rmempty = false;

		o = s.option(form.Value, 'data_dir', zh ? '数据存储目录' : 'Data Directory',
			zh ? '保存配置数据库与已连接设备加密密钥，升级软件包时自动保留。' : 'Stores SQLite database and encrypted host credentials; preserved across upgrades.');
		o.placeholder = '/etc/dkstore';
		o.default = '/etc/dkstore';
		o.rmempty = false;

		return m.render();
	}
});

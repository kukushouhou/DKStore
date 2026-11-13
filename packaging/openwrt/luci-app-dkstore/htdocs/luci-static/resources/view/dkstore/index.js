'use strict';
'require view';
'require rpc';

var callLaunch = rpc.declare({
	object: 'luci.dkstore',
	method: 'launch',
	expect: { '': {} }
});

function isZh() {
	var lang = (document.documentElement.getAttribute('lang') || navigator.language || '').toLowerCase();
	return lang.indexOf('zh') === 0;
}

function buildTargetUrl(port, ticket) {
	var host = window.location.hostname;
	if (host.indexOf(':') !== -1 && host[0] !== '[') {
		host = '[' + host + ']';
	}
	var base = 'http://' + host + ':' + String(port || 8090);
	if (ticket) {
		return base + '/api/v1/auth/handoff?ticket=' + encodeURIComponent(ticket);
	}
	return base + '/';
}

return view.extend({
	handleSaveApply: null,
	handleSave: null,
	handleReset: null,

	load: function() {
		return callLaunch().catch(function() {
			return { running: false, enabled: false, port: 8090, ticket: '' };
		});
	},

	render: function(info) {
		var zh = isZh();
		var running = !!(info && info.running);
		var port = (info && info.port) ? info.port : 8090;
		var ticket = (info && info.ticket) ? info.ticket : '';
		var isHttpsPage = window.location.protocol === 'https:';

		var openNewWindow = function(ev) {
			if (ev) ev.preventDefault();
			var win = window.open('about:blank', '_blank');
			callLaunch().then(function(fresh) {
				var p = (fresh && fresh.port) ? fresh.port : port;
				var tk = (fresh && fresh.ticket) ? fresh.ticket : '';
				var target = buildTargetUrl(p, tk);
				if (win) {
					win.location.replace(target);
				} else {
					window.location.href = target;
				}
			}).catch(function() {
				var fallback = buildTargetUrl(port, '');
				if (win) win.location.replace(fallback);
			});
		};

		var statusBadge = E('span', {
			'style': 'display:inline-flex;align-items:center;gap:6px;padding:2px 10px;border-radius:999px;font-size:12px;font-weight:600;' +
				(running
					? 'background:rgba(82,196,26,0.15);color:#389e0d;'
					: 'background:rgba(255,77,79,0.15);color:#cf1322;')
		}, [
			E('span', {
				'style': 'width:7px;height:7px;border-radius:50%;display:inline-block;' +
					(running ? 'background:#52c41a;' : 'background:#ff4d4f;')
			}),
			running ? (zh ? '运行中 (:' + port + ')' : 'Running (:' + port + ')') : (zh ? '未运行' : 'Stopped')
		]);

		var toolbar = E('div', {
			'style': 'display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:10px;margin:4px 0 10px;'
		}, [
			E('div', { 'style': 'display:flex;align-items:center;gap:10px;' }, [
				E('strong', { 'style': 'font-size:16px;' }, 'DKStore'),
				statusBadge
			]),
			E('div', { 'style': 'display:flex;align-items:center;gap:8px;' }, [
				running ? E('button', {
					'class': 'btn cbi-button cbi-button-action',
					'click': openNewWindow
				}, zh ? '新窗口打开 ↗' : 'Open in New Tab ↗') : E('span'),
				E('a', {
					'class': 'btn cbi-button',
					'href': L.url('admin/services/dkstore/config')
				}, zh ? '服务设置' : 'Service Settings')
			])
		]);

		if (!running) {
			return E('div', { 'class': 'cbi-map' }, [
				toolbar,
				E('div', {
					'class': 'cbi-section',
					'style': 'padding:36px 24px;text-align:center;'
				}, [
					E('p', { 'style': 'margin-bottom:14px;font-size:14px;opacity:0.8;' },
						zh
							? 'DKStore 后台服务当前未启动，请前往「服务设置」启用并保存应用。'
							: 'DKStore service is currently stopped. Enable it under Service Settings.'),
					E('a', {
						'class': 'btn cbi-button cbi-button-apply',
						'href': L.url('admin/services/dkstore/config')
					}, zh ? '前往服务设置' : 'Go to Service Settings')
				])
			]);
		}

		if (isHttpsPage) {
			return E('div', { 'class': 'cbi-map' }, [
				toolbar,
				E('div', {
					'class': 'cbi-section',
					'style': 'padding:36px 24px;text-align:center;'
				}, [
					E('p', { 'style': 'margin-bottom:16px;font-size:14px;opacity:0.85;' },
						zh
							? '当前通过 HTTPS 访问 LuCI，浏览器安全策略限制内嵌 HTTP 面板，请点击下方按钮免密打开 DKStore：'
							: 'LuCI is loaded over HTTPS; click below to open DKStore in a new tab with automatic sign-in:'),
					E('button', {
						'class': 'btn cbi-button cbi-button-action',
						'click': openNewWindow
					}, zh ? '立即打开 DKStore ↗' : 'Open DKStore ↗')
				])
			]);
		}

		return E('div', { 'class': 'cbi-map', 'style': 'margin-bottom:0;' }, [
			toolbar,
			E('iframe', {
				'src': buildTargetUrl(port, ticket),
				'style': 'width:100%;height:calc(100vh - 145px);min-height:680px;border:1px solid rgba(128,128,128,0.22);border-radius:8px;background:#fff;'
			})
		]);
	}
});

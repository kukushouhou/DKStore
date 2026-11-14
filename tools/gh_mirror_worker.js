// gh_mirror_worker.js — DKStore 官方镜像源（gh.h5.gg）专属 Cloudflare Worker 实时转发脚本。
//
// 安全与成本特性：
// 1. 严格白名单锁死：仅允许 GET / HEAD 请求转发 https://github.com/kukushouhou/DKStore/releases/*，
//    其他任何仓库或路径一律直接返回 403，彻底防止外人白嫖额度或代理无关内容。
// 2. 零存储零同步：不保存任何文件副本，实时流式透传 GitHub Releases（完整保留 HTTP Range 断点续传头）。
//
// 部署方式：在 Cloudflare Workers 新建 Worker，粘贴本文件内容并绑定自定义域名 gh.h5.gg 即可。

const ALLOWED_PREFIX = '/https://github.com/kukushouhou/dkstore/releases/';

export default {
  async fetch(request) {
    if (request.method === 'OPTIONS') {
      return new Response(null, {
        status: 204,
        headers: {
          'Access-Control-Allow-Origin': '*',
          'Access-Control-Allow-Methods': 'GET, HEAD, OPTIONS',
          'Access-Control-Allow-Headers': 'Range, User-Agent, Accept',
        },
      });
    }

    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return new Response('Method Not Allowed', { status: 405 });
    }

    const url = new URL(request.url);
    if (url.pathname === '/' || url.pathname === '/health') {
      return new Response('DKStore Official Release Mirror (gh.h5.gg)', {
        status: 200,
        headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      });
    }

    if (!url.pathname.toLowerCase().startsWith(ALLOWED_PREFIX)) {
      return new Response('Forbidden: Only kukushouhou/DKStore releases are allowed.', {
        status: 403,
        headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      });
    }

    const targetUrl = url.pathname.slice(1) + url.search;
    const reqHeaders = new Headers();
    const range = request.headers.get('Range');
    if (range) reqHeaders.set('Range', range);
    reqHeaders.set('User-Agent', request.headers.get('User-Agent') || 'DKStore-Mirror-Worker');

    const upstream = await fetch(targetUrl, {
      method: request.method,
      headers: reqHeaders,
      redirect: 'follow',
    });

    const respHeaders = new Headers(upstream.headers);
    respHeaders.set('Access-Control-Allow-Origin', '*');
    respHeaders.delete('Content-Security-Policy');
    respHeaders.delete('Strict-Transport-Security');

    return new Response(upstream.body, {
      status: upstream.status,
      statusText: upstream.statusText,
      headers: respHeaders,
    });
  },
};

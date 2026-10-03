const http = require('http');

const server = http.createServer((request, response) => {
  let body = '';
  request.on('data', chunk => { body += chunk; });
  request.on('end', () => {
    if (request.url !== '/v1/chat/completions') {
      response.writeHead(404, { 'content-type': 'application/json' });
      response.end(JSON.stringify({ error: { message: 'fixture route not found' } }));
      return;
    }
    const input = JSON.parse(body);
    response.writeHead(200, { 'content-type': 'application/json', 'x-request-id': 'browser-fixture-1' });
    response.end(JSON.stringify({
      id: 'chatcmpl-browser-fixture',
      object: 'chat.completion',
      created: 1,
      model: input.model,
      choices: [{ index: 0, message: { role: 'assistant', content: 'fixture answer' }, finish_reason: 'stop' }],
      usage: { prompt_tokens: 4, completion_tokens: 2, total_tokens: 6 },
    }));
  });
});

server.listen(18081, '127.0.0.1', () => console.log('fixture upstream listening on 18081'));

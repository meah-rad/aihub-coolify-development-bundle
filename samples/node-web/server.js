const http = require('node:http')

const port = Number(process.env.PORT || 3000)
const server = http.createServer((req, res) => {
  if (req.url === '/health') {
    res.writeHead(200, { 'content-type': 'text/plain' })
    res.end('ok')
    return
  }

  res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
  res.end('<main><h1>AI Hub demo is running</h1><p>Your service is isolated and healthy.</p></main>')
})

server.listen(port, '0.0.0.0')

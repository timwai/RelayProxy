Build the Server React console first: cd server/web/frontend && npm install && npm run build
The Server serves only React. Without react_dist/index.html it returns HTTP 503 on /.
This sentinel is committed; generated bundles are not.

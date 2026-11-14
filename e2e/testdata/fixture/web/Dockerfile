FROM busybox:1.36.1
COPY index.html /www/index.html
EXPOSE 80
CMD ["httpd", "-f", "-p", "80", "-h", "/www"]

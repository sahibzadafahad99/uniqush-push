FROM centos:7
MAINTAINER Misha Nasledov <misha@nasledov.com>

RUN yum update -y
RUN /usr/bin/yum -y install golang git gcc make mercurial-hgk wget epel-release nano

ENV GOBIN /tmp/bin
ENV GOPATH /tmp

RUN go get github.com/sahibzadafahad99/uniqush-push

COPY conf/uniqush-push.conf .
COPY uniqush.service /etc/systemd/system/



RUN cp /tmp/bin/uniqush-push /usr/bin \
    && mkdir /etc/uniqush/ \
    && mkdir /opt/uniqush/ \
    && cp ./uniqush-push.conf /etc/uniqush/ \
    && sed -i -e 's/localhost/192.168.0.7/' /etc/uniqush/uniqush-push.conf

COPY uniqush/* /opt/uniqush/

EXPOSE 9898

RUN (cd /lib/systemd/system/sysinit.target.wants/; for i in *; do [ $i == \
systemd-tmpfiles-setup.service ] || rm -f $i; done); \
rm -f /lib/systemd/system/multi-user.target.wants/*;\
rm -f /etc/systemd/system/*.wants/*;\
rm -f /lib/systemd/system/local-fs.target.wants/*; \
rm -f /lib/systemd/system/sockets.target.wants/*udev*; \
rm -f /lib/systemd/system/sockets.target.wants/*initctl*; \
rm -f /lib/systemd/system/basic.target.wants/*;\
rm -f /lib/systemd/system/anaconda.target.wants/*;
VOLUME [ "/sys/fs/cgroup" ]


CMD ["/usr/sbin/init","/usr/bin/uniqush-push"]


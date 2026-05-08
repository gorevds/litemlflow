Name:           litemlflow
Version:        0.4.0
Release:        1.rc1%{?dist}
Summary:        Single-binary experiment tracker, MLflow-compatible, with LLM trace support
License:        ASL 2.0
URL:            https://litemlflow.dev
Source0:        https://github.com/litemlflow/litemlflow/releases/download/v%{version}-rc1/litemlflow-v%{version}-rc1-linux-x86_64
Source1:        litemlflow.service

BuildArch:      x86_64
Requires(pre):  shadow-utils
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

%description
LiteMLflow is a lightweight, self-hosted experiment tracker designed for solo
ML engineers and small teams. Single binary, MLflow-API-compatible, with
first-class LLM trace support.

No database to install. No object store to configure. No reverse proxy.

%prep
# nothing; we work from a pre-built binary.

%build
# nothing.

%install
mkdir -p %{buildroot}/usr/bin %{buildroot}/usr/lib/systemd/system %{buildroot}/var/lib/litemlflow
install -m 0755 %{SOURCE0} %{buildroot}/usr/bin/litemlflow
install -m 0644 %{SOURCE1} %{buildroot}/usr/lib/systemd/system/litemlflow.service

%pre
getent passwd litemlflow >/dev/null || \
    useradd --system --no-create-home --home-dir /var/lib/litemlflow \
            --shell /usr/sbin/nologin litemlflow
mkdir -p /var/lib/litemlflow
chown litemlflow:litemlflow /var/lib/litemlflow
chmod 0750 /var/lib/litemlflow

%post
%systemd_post litemlflow.service

%preun
%systemd_preun litemlflow.service

%postun
%systemd_postun_with_restart litemlflow.service

%files
%attr(0755, root, root) /usr/bin/litemlflow
%attr(0644, root, root) /usr/lib/systemd/system/litemlflow.service
%attr(0750, litemlflow, litemlflow) /var/lib/litemlflow

%changelog
* Thu May 08 2026 LiteMLflow Maintainers <maintainers@litemlflow.dev> - 0.4.0-1.rc1
- Initial RPM release.

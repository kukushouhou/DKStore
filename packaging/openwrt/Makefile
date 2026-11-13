include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-dkstore
PKG_VERSION:=0.2.0
PKG_RELEASE:=1

PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=kukushouhou <https://github.com/kukushouhou>

include $(INCLUDE_DIR)/package.mk

define Package/luci-app-dkstore
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI support for DKStore Docker App Update Center
  DEPENDS:=+(aarch64||x86_64||arm) +ca-bundle
  PROVIDES:=dkstore
endef

define Package/luci-app-dkstore/description
  Cross-Platform Docker application update discovery, one-click service-level upgrade, and rollback center for OpenWrt/iStoreOS, Synology DSM, fnOS, and Linux.
endef

define Package/luci-app-dkstore/conffiles
/etc/config/dkstore
endef

define Build/Compile
	true
endef

define Package/luci-app-dkstore/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/dkstore $(1)/usr/bin/dkstore
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/dkstore.config $(1)/etc/config/dkstore
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/dkstore.init $(1)/etc/init.d/dkstore
	$(CP) ./luci-app-dkstore/root/* $(1)/
	chmod 0755 $(1)/usr/libexec/rpcd/luci.dkstore
	$(INSTALL_DIR) $(1)/www
	$(CP) ./luci-app-dkstore/htdocs/* $(1)/www/
endef

define Package/luci-app-dkstore/postinst
#!/bin/sh
if [ -z "$${IPKG_INSTROOT}" ]; then
	/etc/init.d/dkstore enable >/dev/null 2>&1 || true
	/etc/init.d/dkstore restart >/dev/null 2>&1 || true
	/etc/init.d/rpcd reload >/dev/null 2>&1 || true
	/etc/init.d/uhttpd reload >/dev/null 2>&1 || true
	rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache/* 2>/dev/null || true
fi
exit 0
endef

define Package/luci-app-dkstore/prerm
#!/bin/sh
if [ -z "$${IPKG_INSTROOT}" ] && [ "$$1" = "remove" ]; then
	/etc/init.d/dkstore stop >/dev/null 2>&1 || true
	/etc/init.d/dkstore disable >/dev/null 2>&1 || true
fi
exit 0
endef

$(eval $(call BuildPackage,luci-app-dkstore))

include $(TOPDIR)/rules.mk

PKG_NAME:=dkstore
PKG_VERSION:=0.2.0
PKG_RELEASE:=1

PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=kukushouhou <https://github.com/kukushouhou>

include $(INCLUDE_DIR)/package.mk

define Package/dkstore
  SECTION:=admin
  CATEGORY:=Administration
  TITLE:=DKStore Cross-Platform Docker App Update Center
  DEPENDS:=+(aarch64||x86_64||arm) +ca-bundle
endef

define Package/dkstore/description
  Unified Docker application update discovery, one-click upgrade, and rollback center for OpenWrt/iStoreOS, Synology DSM, and Linux.
endef

define Build/Compile
	true
endef

define Package/dkstore/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/dkstore $(1)/usr/bin/dkstore
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/dkstore.init $(1)/etc/init.d/dkstore
endef

$(eval $(call BuildPackage,dkstore))

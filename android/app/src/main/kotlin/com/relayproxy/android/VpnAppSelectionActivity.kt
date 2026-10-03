package com.relayproxy.android

import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.view.Gravity
import android.view.ViewGroup
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

class VpnAppSelectionActivity : Activity() {
    companion object {
        const val EXTRA_SELECTED = "vpnSelectedPackages"
    }

    private data class AppEntry(val label: String, val packageName: String)

    private val selected = linkedSetOf<String>()
    private lateinit var appList: LinearLayout
    private lateinit var doneButton: Button
    private var entries = emptyList<AppEntry>()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        @Suppress("DEPRECATION")
        selected += intent.getStringArrayListExtra(EXTRA_SELECTED).orEmpty()
            .filter { it != packageName }
        entries = loadApps()
        setContentView(buildUi())
        renderApps("")
    }

    private fun loadApps(): List<AppEntry> {
        val launchIntent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        @Suppress("DEPRECATION")
        val launchable = packageManager.queryIntentActivities(launchIntent, 0)
            .mapNotNull { resolved ->
                val pkg = resolved.activityInfo?.packageName?.trim().orEmpty()
                if (pkg.isBlank() || pkg == packageName) return@mapNotNull null
                AppEntry(resolved.loadLabel(packageManager).toString().ifBlank { pkg }, pkg)
            }
            .distinctBy(AppEntry::packageName)
            .toMutableList()
        val known = launchable.mapTo(hashSetOf(), AppEntry::packageName)
        selected.filterNot { it in known }.forEach { pkg ->
            val label = runCatching {
                @Suppress("DEPRECATION")
                packageManager.getApplicationLabel(packageManager.getApplicationInfo(pkg, 0)).toString()
            }.getOrDefault(pkg)
            launchable += AppEntry(label, pkg)
        }
        return launchable.sortedWith(compareBy(String.CASE_INSENSITIVE_ORDER) { it.label })
    }

    private fun buildUi(): LinearLayout {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(18), dp(28), dp(18), dp(20))
            setBackgroundColor(Color.rgb(246, 248, 252))
        }
        root.addView(TextView(this).apply {
            text = "选择 VPN 应用"
            textSize = 21f
            setTextColor(Color.rgb(15, 23, 42))
            setTypeface(typeface, Typeface.BOLD)
        })
        root.addView(TextView(this).apply {
            text = "这里选择的应用会配合设置页中的“仅选中”或“排除选中”模式使用。"
            textSize = 12.5f
            setTextColor(Color.rgb(100, 116, 139))
            setPadding(0, dp(5), 0, dp(12))
        })

        val search = EditText(this).apply {
            hint = "搜索应用名称或包名"
            setSingleLine(true)
            setPadding(dp(12), 0, dp(12), 0)
            setBackgroundColor(Color.WHITE)
            addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                    renderApps(s?.toString().orEmpty())
                }
                override fun afterTextChanged(s: Editable?) = Unit
            })
        }
        root.addView(search, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(48)))

        val quick = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.END
            addView(actionButton("全选") {
                selected += entries.map(AppEntry::packageName)
                renderApps(search.text.toString())
                updateDoneButton()
            })
            addView(actionButton("清空") {
                selected.clear()
                renderApps(search.text.toString())
                updateDoneButton()
            })
        }
        root.addView(quick)

        appList = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        root.addView(
            ScrollView(this).apply { addView(appList) },
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f),
        )

        doneButton = Button(this).apply {
            setAllCaps(false)
            setOnClickListener {
                setResult(
                    RESULT_OK,
                    Intent().putStringArrayListExtra(EXTRA_SELECTED, ArrayList(selected.sorted())),
                )
                finish()
            }
        }
        updateDoneButton()
        root.addView(doneButton, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(50)))
        return root
    }

    private fun actionButton(label: String, action: () -> Unit) = TextView(this).apply {
        text = label
        textSize = 13f
        setTextColor(Color.rgb(37, 99, 235))
        setTypeface(typeface, Typeface.BOLD)
        setPadding(dp(16), dp(12), dp(4), dp(12))
        setOnClickListener { action() }
    }

    private fun renderApps(query: String) {
        if (!::appList.isInitialized) return
        val normalized = query.trim().lowercase()
        appList.removeAllViews()
        entries.asSequence()
            .filter {
                normalized.isBlank() || it.label.lowercase().contains(normalized) ||
                    it.packageName.lowercase().contains(normalized)
            }
            .forEach { entry ->
                appList.addView(CheckBox(this).apply {
                    text = "${entry.label}\n${entry.packageName}"
                    textSize = 13.5f
                    setTextColor(Color.rgb(15, 23, 42))
                    setPadding(dp(4), dp(8), dp(4), dp(8))
                    isChecked = entry.packageName in selected
                    setOnCheckedChangeListener { _, checked ->
                        if (checked) selected += entry.packageName else selected -= entry.packageName
                        updateDoneButton()
                    }
                })
            }
    }

    private fun updateDoneButton() {
        if (::doneButton.isInitialized) doneButton.text = "完成（已选 ${selected.size} 个）"
    }

    private fun dp(value: Int) = (value * resources.displayMetrics.density + 0.5f).toInt()
}

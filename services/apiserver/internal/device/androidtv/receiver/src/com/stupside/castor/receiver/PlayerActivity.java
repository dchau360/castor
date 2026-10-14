package com.stupside.castor.receiver;

import android.app.Activity;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.graphics.Color;
import android.media.MediaPlayer;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.Gravity;
import android.view.WindowManager;
import android.widget.FrameLayout;
import android.widget.VideoView;

// Plays the URL it is started with; castor reads and drives it through ordered CONTROL broadcasts over adb.
public class PlayerActivity extends Activity {
    static final String ACTION_CONTROL = "com.stupside.castor.receiver.CONTROL";
    static final String EXTRA_OP = "op";
    static final String EXTRA_FORMAT = "format";

    private VideoView video;
    private String state = "loading";

    private final BroadcastReceiver control = new BroadcastReceiver() {
        @Override
        public void onReceive(Context context, Intent intent) {
            String op = intent.getStringExtra(EXTRA_OP);
            if ("pause".equals(op) && !isOver()) {
                video.pause();
                state = "paused";
            } else if ("resume".equals(op) && !isOver()) {
                video.start();
                state = "playing";
            }
            setResultCode(1);
            setResultData(state);
        }
    };

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);

        FrameLayout root = new FrameLayout(this);
        root.setBackgroundColor(Color.BLACK);
        video = new VideoView(this);
        root.addView(video, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT, Gravity.CENTER));
        setContentView(root);

        video.setOnPreparedListener(mp -> {
            if (!"paused".equals(state)) {
                state = "playing";
                video.start();
            }
        });
        video.setOnCompletionListener(mp -> {
            state = "ended";
            finish();
        });
        video.setOnErrorListener((mp, what, extra) -> {
            state = "failed:" + what + "/" + extra;
            return true;
        });

        IntentFilter filter = new IntentFilter(ACTION_CONTROL);
        if (Build.VERSION.SDK_INT >= 33) {
            registerReceiver(control, filter, Context.RECEIVER_EXPORTED);
        } else {
            registerReceiver(control, filter);
        }
        load(getIntent());
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        load(intent);
    }

    @Override
    protected void onDestroy() {
        unregisterReceiver(control);
        video.stopPlayback();
        super.onDestroy();
    }

    private boolean isOver() {
        return "ended".equals(state) || state.startsWith("failed");
    }

    private void load(Intent intent) {
        Uri uri = intent.getData();
        if (uri == null) {
            state = "failed:no url";
            return;
        }
        // The platform player only takes the HLS path for a URL that names m3u8; a fragment never reaches the server.
        if ("hls".equals(intent.getStringExtra(EXTRA_FORMAT)) && !uri.toString().contains("m3u8")) {
            uri = Uri.parse(uri.toString() + "#castor.m3u8");
        }
        state = "loading";
        video.setVideoURI(uri);
    }
}
